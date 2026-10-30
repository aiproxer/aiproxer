package runtime_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/affinity"
	"github.com/aiproxer/aiproxer/internal/core/affinity/memorystore"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestExecutor_adminOverrideSelectorClassMatchesClient(t *testing.T) {
	t.Parallel()
	aliases, err := routing.NewAliasResolver([]routing.ModelAliasRule{
		{Pattern: `^cheap$`, Replacement: "aliasbe:m"},
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name           string
		selector       string
		defaultBackend string
		aliases        *routing.AliasResolver
		affinity       bool
		turns          int
		race           bool
		failFirst      string
		wantBackends   []string
		wantRace       []string
	}{
		{name: "direct", selector: "dirbe:m", wantBackends: []string{"dirbe"}},
		{name: "modelOnly", selector: "gpt-4", defaultBackend: "modelbe", wantBackends: []string{"modelbe"}},
		{name: "alias", selector: "cheap", aliases: aliases, wantBackends: []string{"aliasbe"}},
		{name: "failover", selector: "bad:m|ok:m", failFirst: "bad", wantBackends: []string{"bad", "ok"}},
		{name: "weighted", selector: "[weight=1]w1:m^[weight=1]w2:m"},
		{name: "race", selector: "rleft:m!rright:m", race: true, wantRace: []string{"rleft", "rright"}},
		{name: "ttft", selector: "{ttft_timeout=60}ttftbe:m", wantBackends: []string{"ttftbe"}},
		{name: "affinity", selector: "{affinity=session}[weight=1]affa:m^[weight=1]affb:m", affinity: true, turns: 2},
		{name: "first", selector: "[first]cheapfirst:m^[weight=100]expensive:m", wantBackends: []string{"cheapfirst"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			turns := max(tc.turns, 1)
			clientOpens := runSelectorClassTurns(t, selectorClassRun{
				selector:       tc.selector,
				clientSelector: tc.selector,
				override:       "",
				defaultBackend: tc.defaultBackend,
				aliases:        tc.aliases,
				affinity:       tc.affinity,
				turns:          turns,
				failFirst:      tc.failFirst,
				raceBackends:   tc.wantRace,
			})
			adminOpens := runSelectorClassTurns(t, selectorClassRun{
				selector:       tc.selector,
				clientSelector: overrideClientSelector,
				override:       tc.selector,
				defaultBackend: tc.defaultBackend,
				aliases:        tc.aliases,
				affinity:       tc.affinity,
				turns:          turns,
				failFirst:      tc.failFirst,
				raceBackends:   tc.wantRace,
			})
			if tc.race {
				assertOpenBackendSet(t, clientOpens, tc.wantRace)
				assertOpenBackendSet(t, adminOpens, tc.wantRace)
			} else if len(tc.wantBackends) > 0 {
				assertOpenBackends(t, clientOpens, tc.wantBackends)
				assertOpenBackends(t, adminOpens, tc.wantBackends)
			}
			assertOpenModelsMatch(t, clientOpens, adminOpens)
			if tc.affinity {
				if len(clientOpens) < 2 || clientOpens[0].backend != clientOpens[1].backend {
					t.Fatalf("client affinity must stick across turns: %+v", clientOpens)
				}
				if len(adminOpens) < 2 || adminOpens[0].backend != adminOpens[1].backend {
					t.Fatalf("admin affinity must stick across turns: %+v", adminOpens)
				}
			}
			for i, o := range adminOpens {
				if o.selector != tc.selector {
					t.Fatalf("admin open[%d] selector=%q want %q", i, o.selector, tc.selector)
				}
			}
		})
	}
}

func TestExecutor_replaceOverrideBetweenTurnsThenClearRestoresClient(t *testing.T) {
	t.Parallel()
	cap := &routeOpenCapture{}
	ex, st := routePlanLifetimeExecutor(t, map[string]execbackend.Backend{
		"clientbe": overrideStreamingBackend(cap, "clientbe"),
		"firstbe":  overrideStreamingBackend(cap, "firstbe"),
		"secondbe": overrideStreamingBackend(cap, "secondbe"),
	})
	seed := seedOverrideALeg(t, ex, st, "ov-class-replace", "firstbe:m")
	resetRouteOpenCapture(cap)

	collectExecute(t, ex, context.Background(), resumeOverrideCall(seed, overrideClientSelector))
	got := cap.snapshot()
	if len(got) != 1 || got[0].backend != "firstbe" || got[0].selector != "firstbe:m" {
		t.Fatalf("revision-1 turn: %+v", got)
	}

	if _, err := st.Replace(context.Background(), seed.Session.ALegID, "secondbe:m", time.Now().UTC()); err != nil {
		t.Fatalf("replace: %v", err)
	}
	resetRouteOpenCapture(cap)
	collectExecute(t, ex, context.Background(), resumeOverrideCall(seed, overrideClientSelector))
	got = cap.snapshot()
	if len(got) != 1 || got[0].backend != "secondbe" || got[0].selector != "secondbe:m" {
		t.Fatalf("revision-2 turn: %+v", got)
	}

	if _, err := st.Clear(context.Background(), seed.Session.ALegID, time.Now().UTC()); err != nil {
		t.Fatalf("clear: %v", err)
	}
	resetRouteOpenCapture(cap)
	collectExecute(t, ex, context.Background(), resumeOverrideCall(seed, overrideClientSelector))
	got = cap.snapshot()
	if len(got) != 1 || got[0].backend != "clientbe" || got[0].selector != overrideClientSelector {
		t.Fatalf("after clear: %+v want client %q", got, overrideClientSelector)
	}
}

type selectorClassRun struct {
	selector       string
	clientSelector string
	override       string
	defaultBackend string
	aliases        *routing.AliasResolver
	affinity       bool
	turns          int
	failFirst      string
	raceBackends   []string
}

func runSelectorClassTurns(t *testing.T, run selectorClassRun) []routeOpen {
	t.Helper()
	cap := &routeOpenCapture{}
	backends := map[string]execbackend.Backend{
		"clientbe":   overrideStreamingBackend(cap, "clientbe"),
		"dirbe":      overrideStreamingBackend(cap, "dirbe"),
		"modelbe":    overrideStreamingBackend(cap, "modelbe"),
		"aliasbe":    overrideStreamingBackend(cap, "aliasbe"),
		"ok":         overrideStreamingBackend(cap, "ok"),
		"w1":         overrideStreamingBackend(cap, "w1"),
		"w2":         overrideStreamingBackend(cap, "w2"),
		"rleft":      overrideStreamingBackend(cap, "rleft"),
		"rright":     overrideStreamingBackend(cap, "rright"),
		"ttftbe":     overrideStreamingBackend(cap, "ttftbe"),
		"affa":       overrideStreamingBackend(cap, "affa"),
		"affb":       overrideStreamingBackend(cap, "affb"),
		"cheapfirst": overrideStreamingBackend(cap, "cheapfirst"),
		"expensive":  overrideStreamingBackend(cap, "expensive"),
		"hleft":      overrideStreamingBackend(cap, "hleft"),
		"hright":     overrideStreamingBackend(cap, "hright"),
	}
	if run.failFirst != "" {
		name := run.failFirst
		backends[name] = execbackend.Backend{
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(_ context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				cap.count.Add(1)
				cap.mu.Lock()
				cap.opens = append(cap.opens, routeOpen{backend: name, selector: call.Route.Selector, model: cand.Primary.Model})
				cap.mu.Unlock()
				return nil, aipapi.RecoverablePreOutputError(errors.New("temp"))
			},
		}
	}
	ex, st := routePlanLifetimeExecutor(t, backends)
	ex.Rand = routing.NewSeededRng(1)
	ex.DefaultBackend = run.defaultBackend
	ex.SelectorAliases = run.aliases
	if run.affinity {
		ex.AffinityStore = memorystore.New()
	}
	var call *aipapi.Call
	if run.override != "" {
		call = seedOverrideALeg(t, ex, st, "ov-class-"+run.selector, run.override)
		resetRouteOpenCapture(cap)
		call = resumeOverrideCall(call, run.clientSelector)
	} else {
		call = routePlanLifetimeCall(run.clientSelector, "ov-class-client-"+run.selector)
	}
	if len(run.raceBackends) > 0 {
		cap.entered = make(chan struct{}, 8)
		cap.hold = make(chan struct{})
		var releaseHold sync.Once
		release := func() { releaseHold.Do(func() { close(cap.hold) }) }
		defer release()
		current := call
		for i := 0; i < run.turns; i++ {
			turn := current
			turnCtx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			var stream aipapi.EventStream
			go func() {
				s, err := ex.Execute(turnCtx, turn)
				stream = s
				done <- err
			}()
			waitHeldRouteOpens(t, cap, run.raceBackends, cancel, release, done)
			release()
			if err := <-done; err != nil {
				cancel()
				t.Fatalf("race execute: %v", err)
			}
			if _, err := aipapi.Collect(context.Background(), stream); err != nil {
				cancel()
				t.Fatalf("race collect: %v", err)
			}
			cancel()
			if i+1 < run.turns {
				current = resumeOverrideCall(current, run.clientSelector)
			}
		}
		return cap.snapshot()
	}
	current := call
	for i := 0; i < run.turns; i++ {
		collectExecute(t, ex, context.Background(), current)
		if i+1 < run.turns {
			current = resumeOverrideCall(current, run.clientSelector)
		}
	}
	return cap.snapshot()
}

func assertOpenBackends(t *testing.T, got []routeOpen, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("opens=%+v want backends %v", got, want)
	}
	for i, w := range want {
		if got[i].backend != w {
			t.Fatalf("open[%d] backend=%q want %q (%+v)", i, got[i].backend, w, got)
		}
	}
}

func assertOpenBackendSet(t *testing.T, got []routeOpen, want []string) {
	t.Helper()
	seen := map[string]bool{}
	for _, o := range got {
		seen[o.backend] = true
	}
	for _, w := range want {
		if !seen[w] {
			t.Fatalf("missing race backend %q in %+v", w, got)
		}
	}
}

func assertOpenModelsMatch(t *testing.T, client, admin []routeOpen) {
	t.Helper()
	if len(client) != len(admin) {
		t.Fatalf("open count client=%d admin=%d", len(client), len(admin))
	}
	clientModels := map[string]int{}
	adminModels := map[string]int{}
	for _, o := range client {
		clientModels[o.backend+":"+o.model]++
	}
	for _, o := range admin {
		adminModels[o.backend+":"+o.model]++
	}
	for k, n := range clientModels {
		if adminModels[k] != n {
			t.Fatalf("model/backend %q count client=%d admin=%d", k, n, adminModels[k])
		}
	}
}

func TestExecutor_setReplaceClearDoesNotResetAffinity(t *testing.T) {
	t.Parallel()
	const affinitySel = "{affinity=session}[weight=1]affa:m^[weight=1]affb:m"
	cap := &routeOpenCapture{}
	affStore := memorystore.New()
	ex, st := routePlanLifetimeExecutor(t, map[string]execbackend.Backend{
		"clientbe": overrideStreamingBackend(cap, "clientbe"),
		"adminbe":  overrideStreamingBackend(cap, "adminbe"),
		"affa":     overrideStreamingBackend(cap, "affa"),
		"affb":     overrideStreamingBackend(cap, "affb"),
	})
	ex.AffinityStore = affStore
	ex.Rand = &sequenceRng{vals: []int{1}}
	seed := seedOverrideALeg(t, ex, st, "ov-aff-preserve", "")
	resetRouteOpenCapture(cap)
	collectExecute(t, ex, context.Background(), resumeOverrideCall(seed, affinitySel))
	first := cap.snapshot()
	if len(first) != 1 {
		t.Fatalf("first affinity turn: %+v", first)
	}
	key := affinity.Key{Scope: affinity.ScopeSession, ID: seed.Session.AuthoritativeSessionID}
	before, ok, err := affStore.Get(context.Background(), key)
	if err != nil || !ok || before.BackendID != first[0].backend {
		t.Fatalf("affinity binding after first turn: ok=%v err=%v %+v opens=%+v", ok, err, before, first)
	}

	if _, err := st.Replace(context.Background(), seed.Session.ALegID, overrideAdminSelector, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Clear(context.Background(), seed.Session.ALegID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	after, ok, err := affStore.Get(context.Background(), key)
	if err != nil || !ok || after.BackendID != before.BackendID || after.CandidateKey != before.CandidateKey {
		t.Fatalf("set/replace/clear must not reset affinity: before=%+v after=%+v ok=%v err=%v", before, after, ok, err)
	}

	resetRouteOpenCapture(cap)
	collectExecute(t, ex, context.Background(), resumeOverrideCall(seed, affinitySel))
	got := cap.snapshot()
	if len(got) != 1 || got[0].backend != first[0].backend {
		t.Fatalf("after clear, affinity selector must reuse binding: first=%+v later=%+v", first, got)
	}
}
