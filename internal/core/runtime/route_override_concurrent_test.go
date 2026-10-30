package runtime_test

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type concurrentTurnKey struct{}

type concurrentOpen struct {
	turn     int
	backend  string
	selector string
}

type concurrentOpenLog struct {
	mu    sync.Mutex
	opens []concurrentOpen
}

func (c *concurrentOpenLog) record(ctx context.Context, backend, selector string) {
	turn, _ := ctx.Value(concurrentTurnKey{}).(int)
	c.mu.Lock()
	c.opens = append(c.opens, concurrentOpen{turn: turn, backend: backend, selector: selector})
	c.mu.Unlock()
}

func (c *concurrentOpenLog) reset() {
	c.mu.Lock()
	c.opens = nil
	c.mu.Unlock()
}

func (c *concurrentOpenLog) snapshot() []concurrentOpen {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]concurrentOpen, len(c.opens))
	copy(out, c.opens)
	return out
}

func concurrentStreamingBackend(log *concurrentOpenLog, name string) execbackend.Backend {
	return execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		Open: func(ctx context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			log.record(ctx, name, call.Route.Selector)
			return routePlanLifetimeTextStream(), nil
		},
	}
}

func concurrentFailingBackend(log *concurrentOpenLog, name string) execbackend.Backend {
	return execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		Open: func(ctx context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			log.record(ctx, name, call.Route.Selector)
			return nil, aipapi.RecoverablePreOutputError(errors.New("temp"))
		},
	}
}

func concurrentThinkerBackend(log *concurrentOpenLog, name string, newStream func() aipapi.ManagedEventStream) execbackend.Backend {
	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools)
	return execbackend.Backend{
		Caps: caps,
		TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
			Operation: aipapi.OperationOpenAIChatCompletions,
			Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
		}),
		Open: func(ctx context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			log.record(ctx, name, call.Route.Selector)
			return newStream(), nil
		},
	}
}

func TestExecutor_concurrentAdmissionWhileOverrideMutations(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		selA     string
		selB     string
		thinker  bool
		backends func(log *concurrentOpenLog) map[string]execbackend.Backend
	}{
		{
			name: "failover",
			selA: "bad:m|ok:m",
			selB: "bad2:m|ok2:m",
			backends: func(log *concurrentOpenLog) map[string]execbackend.Backend {
				return map[string]execbackend.Backend{
					"clientbe": concurrentStreamingBackend(log, "clientbe"),
					"bad":      concurrentFailingBackend(log, "bad"),
					"ok":       concurrentStreamingBackend(log, "ok"),
					"bad2":     concurrentFailingBackend(log, "bad2"),
					"ok2":      concurrentStreamingBackend(log, "ok2"),
				}
			},
		},
		{
			name: "race",
			selA: "left:m!right:m",
			selB: "left2:m!right2:m",
			backends: func(log *concurrentOpenLog) map[string]execbackend.Backend {
				return map[string]execbackend.Backend{
					"clientbe": concurrentStreamingBackend(log, "clientbe"),
					"left":     concurrentStreamingBackend(log, "left"),
					"right":    concurrentStreamingBackend(log, "right"),
					"left2":    concurrentStreamingBackend(log, "left2"),
					"right2":   concurrentStreamingBackend(log, "right2"),
				}
			},
		},
		{
			name:    "thinker",
			selA:    "[thinker]thinker-be:m^exec-be:m",
			selB:    "[thinker]thinker2-be:m^exec2-be:m",
			thinker: true,
			backends: func(log *concurrentOpenLog) map[string]execbackend.Backend {
				return map[string]execbackend.Backend{
					"clientbe": concurrentStreamingBackend(log, "clientbe"),
					"exec-be": concurrentThinkerBackend(log, "exec-be", func() aipapi.ManagedEventStream {
						return executorTextStream("executor answer")
					}),
					"thinker-be": concurrentThinkerBackend(log, "thinker-be", func() aipapi.ManagedEventStream {
						return thinkerMemoStream("plan")
					}),
					"exec2-be": concurrentThinkerBackend(log, "exec2-be", func() aipapi.ManagedEventStream {
						return executorTextStream("executor 2")
					}),
					"thinker2-be": concurrentThinkerBackend(log, "thinker2-be", func() aipapi.ManagedEventStream {
						return thinkerMemoStream("plan2")
					}),
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			log := &concurrentOpenLog{}
			var ex *runtime.Executor
			var st *b2bua.MemoryStore
			if tc.thinker {
				ex, st = thinkerOverrideExecutor(t, &routeOpenCapture{}, false)
				ex.Backends = tc.backends(log)
				ex.InterleavedProcessor = runtime.NewTestInterleavedProcessor(t, interleavedthinking.Config{
					Instructions:          "Think step by step.",
					StreamToClient:        "hidden",
					MaxMemoBytes:          4096,
					RegularTurnsRemaining: 64,
				}, runtime.GetTestMemoStore(ex))
			} else {
				ex, st = routePlanLifetimeExecutor(t, tc.backends(log))
			}
			ex.RouteOverrideReader = st

			const (
				sessions = 6
				turns    = 4
			)
			seeds := make([]*aipapi.Call, sessions)
			for i := range sessions {
				key := "ov-conc-" + tc.name + "-" + strconv.Itoa(i)
				if tc.thinker {
					seeds[i] = seedThinkerSession(t, ex, st, false, key)
					if _, err := st.Replace(context.Background(), seeds[i].Session.ALegID, tc.selA, time.Now().UTC()); err != nil {
						t.Fatalf("seed replace: %v", err)
					}
				} else {
					seeds[i] = seedOverrideALeg(t, ex, st, key, tc.selA)
				}
			}
			log.reset()

			stop := make(chan struct{})
			var mutators sync.WaitGroup
			mutators.Go(func() {
				n := 0
				for {
					select {
					case <-stop:
						return
					default:
					}
					seed := seeds[n%sessions]
					now := time.Now().UTC()
					switch n % 3 {
					case 0:
						_, _ = st.Replace(context.Background(), seed.Session.ALegID, tc.selA, now)
					case 1:
						_, _ = st.Replace(context.Background(), seed.Session.ALegID, tc.selB, now)
					default:
						_, _ = st.Clear(context.Background(), seed.Session.ALegID, now)
					}
					n++
				}
			})

			clientSel := overrideClientSelector
			if tc.thinker {
				clientSel = tc.selA
			}
			allowed := map[string]struct{}{
				clientSel: {},
				tc.selA:   {},
				tc.selB:   {},
			}
			var turnID atomic.Int64
			var workers sync.WaitGroup
			for i := range sessions {
				seed := seeds[i]
				workers.Go(func() {
					for range turns {
						id := int(turnID.Add(1))
						ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), concurrentTurnKey{}, id), 20*time.Second)
						var call *aipapi.Call
						if tc.thinker {
							call = interleavedBaseCall(clientSel)
							resumeInterleavedCall(seed, call)
						} else {
							call = resumeOverrideCall(seed, clientSel)
						}
						stream, err := ex.Execute(ctx, call)
						if err != nil {
							cancel()
							t.Errorf("execute turn %d: %v", id, err)
							return
						}
						if _, err := aipapi.Collect(ctx, stream); err != nil {
							cancel()
							t.Errorf("collect turn %d: %v", id, err)
							return
						}
						cancel()
					}
				})
			}
			workers.Wait()
			close(stop)
			mutators.Wait()
			if t.Failed() {
				return
			}

			byTurn := map[int][]concurrentOpen{}
			for _, o := range log.snapshot() {
				if o.turn == 0 {
					t.Fatalf("open missing turn id: %+v", o)
				}
				if _, ok := allowed[o.selector]; !ok {
					t.Fatalf("unexpected selector %q on %+v", o.selector, o)
				}
				byTurn[o.turn] = append(byTurn[o.turn], o)
			}
			if len(byTurn) == 0 {
				t.Fatal("expected per-turn B-leg opens")
			}
			for id, opens := range byTurn {
				sel := opens[0].selector
				for _, o := range opens[1:] {
					if o.selector != sel {
						t.Fatalf("turn %d torn selectors: %+v", id, opens)
					}
				}
			}
		})
	}
}
