package runtime

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/billingport"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

type isoStream struct {
	gate   <-chan struct{}
	events []aipapi.Event
	done   chan struct{}
	once   sync.Once
	n      int
}

type isoSink struct {
	c *compactionContinuityBillingCapture
}

func (s isoSink) AppendCall(ctx context.Context, r usageevidence.CallUsageRecord) error {
	return s.c.appendCall(ctx, r)
}

func (s isoSink) AppendLeg(ctx context.Context, r usageevidence.CallLegUsageRecord) error {
	return s.c.appendLeg(ctx, r)
}

func (s *isoStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if s.n == 0 && s.gate != nil {
		select {
		case <-s.gate:
		case <-ctx.Done():
			return aipapi.Event{}, ctx.Err()
		case <-s.done:
			return aipapi.Event{}, io.EOF
		}
	}
	if err := ctx.Err(); err != nil {
		return aipapi.Event{}, err
	}
	if s.n >= len(s.events) {
		return aipapi.Event{}, io.EOF
	}
	ev := s.events[s.n]
	s.n++
	return ev, nil
}

func (s *isoStream) Close() error { s.once.Do(func() { close(s.done) }); return nil }

func (s *isoStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	_ = s.Close()
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

func isoEvents(in, out int) []aipapi.Event {
	return []aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventMessageStarted}, {Kind: aipapi.EventTextDelta, Delta: "x"}, {Kind: aipapi.EventUsageDelta, InputTokens: in, OutputTokens: out, TotalTokens: in + out, UsagePresence: aipapi.UsagePresence{InputTokens: true, OutputTokens: true, TotalTokens: true}, Accounting: aipapi.UsageAccountingMetadata{Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported, Authority: aipapi.UsageAuthorityAuthoritative, DedupeKey: "iso"}}, {Kind: aipapi.EventResponseFinished}}
}

func isoExecutor(t *testing.T, cap *compactionContinuityBillingCapture, st *b2bua.MemoryStore, ss *memory.Store) *Executor {
	t.Helper()
	mgr := testSecureManager(t, ss, st)
	ex := TestExecutor()
	ex.Store = st
	ex.SecureSession = mgr
	ex.SecureSessionRecorder = mustRecorder(t, ss)
	ex.Bus = hooks.New(hooks.Config{})
	ex.SyntheticLocalPrincipal = true
	ex.Rand = routing.NewSeededRng(7)
	ex.MaxAttempts = 2
	ex.Now = func() time.Time { return time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC) }
	ex.BillingIdentity = testBillingIdentity()
	ex.BillingExposureAdmission = exposureAdmissionFunc(func(_ context.Context, in BillingExposureAdmissionInput) (billingport.CallExposure, error) {
		return billingport.CallExposure{AccountID: "acct", CallID: in.CallID, PricingRef: usageevidence.VersionRef{ID: "p", Version: "1"}, ChargePolicyRef: usageevidence.VersionRef{ID: "c", Version: "1"}, Status: billingport.ExposureOpen}, nil
	})
	ex.TerminalUsageSink = isoSink{cap}
	return ex
}

func mustRecorder(t *testing.T, ss *memory.Store) *app.Recorder {
	t.Helper()
	r, e := app.NewRecorder(ss)
	if e != nil {
		t.Fatal(e)
	}
	return r
}

func isoCtx() context.Context {
	return scope.WithScope(context.Background(), scope.PrincipalScopeView{SubjectKind: scope.SubjectHuman, PrincipalID: scope.Known(syntheticLocalPrincipalID), Origin: scope.OriginClient})
}

func isoParent() *aipapi.Call {
	return &aipapi.Call{Session: aipapi.SessionRef{ClientSessionID: "client", ContinuityKey: "parent"}, Route: aipapi.RouteIntent{Selector: "primary:m"}, Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("p")}}}}
}

func isoChild(p *aipapi.Call, sel string) auxiliary.Request {
	return auxiliary.Request{Call: &aipapi.Call{Session: p.Session, Route: aipapi.RouteIntent{Selector: sel}, Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("extract")}}}}, Role: "compaction_continuity_extractor", Visibility: "private", ParentTraceID: "trace", ParentALegID: p.Session.ALegID, ParentBranchBinding: "branch", SessionMode: auxiliary.SessionModeDetached}
}

func TestCompactionContinuityDetachedConcurrentSessionIsolation(t *testing.T) {
	cap := &compactionContinuityBillingCapture{}
	st, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	ss := memory.New(memory.Options{SimulateDurable: true})
	ex := isoExecutor(t, cap, st, ss)
	open := make(chan struct{})
	release := make(chan struct{})
	attempts := make(chan struct{}, 2)
	ex.Backends = map[string]execbackend.Backend{"primary": {Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming), Open: func(ctx context.Context, c aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		cap.mu.Lock()
		cap.opens = append(cap.opens, compactionContinuityOpen{call: aipapi.CloneCall(c), auxiliary: false, scope: mustScope(ctx)})
		cap.mu.Unlock()
		close(open)
		return &isoStream{gate: release, events: isoEvents(11, 5), done: make(chan struct{})}, nil
	}}, "fail": {Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming), Open: func(ctx context.Context, c aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		cap.mu.Lock()
		cap.opens = append(cap.opens, compactionContinuityOpen{call: aipapi.CloneCall(c), auxiliary: true, scope: mustScope(ctx)})
		cap.mu.Unlock()
		attempts <- struct{}{}
		return nil, aipapi.RecoverablePreOutputError(errors.New("fail"))
	}}, "good": {Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming), Open: func(ctx context.Context, c aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		cap.mu.Lock()
		cap.opens = append(cap.opens, compactionContinuityOpen{call: aipapi.CloneCall(c), auxiliary: true, scope: mustScope(ctx)})
		cap.mu.Unlock()
		attempts <- struct{}{}
		return aipapi.NewFixedEventStream(isoEvents(3, 2)), nil
	}}}
	ctx := isoCtx()
	p := isoParent()
	done := make(chan error, 1)
	go func() {
		s, e := ex.Execute(ctx, p)
		if e == nil {
			_, e = aipapi.Collect(ctx, s)
		}
		done <- e
	}()
	<-open
	sid := p.Session.AuthoritativeSessionID
	before, _ := ss.LoadByID(ctx, domain.SessionID(sid))
	tin, _ := ss.Transcript(ctx, domain.SessionID(sid), domain.ReadOptions{})
	in0, out0, _ := ss.UsageTokenTotals(ctx, domain.SessionID(sid))
	child := isoChild(p, "fail:m^good:m")
	ce := make(chan error, 1)
	go func() {
		s, e := (&auxiliaryClientForExecutor{executor: ex}).Stream(ctx, child)
		if e == nil {
			_, e = aipapi.Collect(ctx, s)
		}
		ce <- e
	}()
	<-attempts
	<-attempts
	if e := <-ce; e != nil {
		t.Fatal(e)
	}
	close(release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	after, _ := ss.LoadByID(ctx, domain.SessionID(sid))
	tin1, _ := ss.Transcript(ctx, domain.SessionID(sid), domain.ReadOptions{})
	in1, out1, _ := ss.UsageTokenTotals(ctx, domain.SessionID(sid))
	if in0 != 0 || out0 != 0 || in1 != 11 || out1 != 5 || len(tin1) <= len(tin) || !after.ResumeEligible || after.ResumeFingerprint != before.ResumeFingerprint || !after.LastActivityAt.Equal(before.LastActivityAt) {
		t.Fatalf("parent state changed: before=%+v after=%+v usage=%d/%d transcript=%d/%d", before, after, in1, out1, len(tin), len(tin1))
	}
	calls, legs, opens := cap.snapshot()
	var pc, cc usageevidence.CallUsageRecord
	var childA string
	for _, o := range opens {
		if o.auxiliary {
			childA = o.call.Session.ALegID
			if o.call.Session.ClientSessionID != "" || o.call.Session.AuthoritativeSessionID != "" || o.call.Session.ResumeToken != "" {
				t.Fatalf("child headers leaked: %+v", o.call.Session)
			}
		}
	}
	for _, c := range calls {
		if c.Workload.IsAuxiliary() {
			cc = c
		} else {
			pc = c
		}
	}
	if pc.CallID == cc.CallID || pc.ALegID == cc.ALegID || childA == p.Session.ALegID || len(legs) != 3 {
		t.Fatalf("cross-settlement/lineage calls=%+v legs=%d childA=%q", calls, len(legs), childA)
	}
	for _, c := range []usageevidence.CallUsageRecord{pc, cc} {
		var ls []usageevidence.CallLegUsageRecord
		for _, l := range legs {
			if l.CallID == c.CallID && l.Workload == c.Workload {
				ls = append(ls, l)
			}
		}
		sc, e := c.Seal()
		if e != nil {
			t.Fatal(e)
		}
		sl := make([]usageevidence.CallLegUsageRecord, len(ls))
		for i, l := range ls {
			sl[i], e = l.Seal()
			if e != nil {
				t.Fatal(e)
			}
		}
		if _, e = usageevidence.JoinCompleteCall(sc, sl); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := ss.LoadByALegID(ctx, childA); !errors.Is(e, domain.ErrSessionNotFound) {
		t.Fatalf("child secure row: %v", e)
	}
	as, _ := st.LoadAttempts(ctx, childA)
	if len(as) != 2 || as[0].Seq != 1 || as[1].Seq != 2 {
		t.Fatalf("attempt lineage: %+v", as)
	}
}

func TestCompactionContinuityDetachedCancellationTimeoutDoNotCrossBranch(t *testing.T) {
	for _, tc := range []struct {
		name    string
		parent  bool
		timeout bool
		want    error
	}{{"child-cancel", false, false, context.Canceled}, {"parent-cancel", true, false, context.Canceled}, {"timeout", false, true, context.DeadlineExceeded}} {
		t.Run(tc.name, func(t *testing.T) {
			cap := &compactionContinuityBillingCapture{}
			st, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
			ss := memory.New(memory.Options{SimulateDurable: true})
			ex := isoExecutor(t, cap, st, ss)
			gate := make(chan struct{})
			opened := make(chan struct{})
			ex.Backends = map[string]execbackend.Backend{"primary": {Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming), Open: func(_ context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream(isoEvents(11, 5)), nil
			}}, "extract": {Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming), Open: func(_ context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				close(opened)
				return &isoStream{gate: gate, events: isoEvents(3, 2), done: make(chan struct{})}, nil
			}}}
			parentCtx, cancelParent := context.WithCancel(isoCtx())
			defer cancelParent()
			p := isoParent()
			s, e := ex.Execute(parentCtx, p)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = aipapi.Collect(parentCtx, s); e != nil {
				t.Fatal(e)
			}
			before, _ := ss.LoadByID(parentCtx, domain.SessionID(p.Session.AuthoritativeSessionID))
			childCtx := parentCtx
			cancelChild := func() {}
			if tc.timeout {
				childCtx, cancelChild = context.WithTimeout(parentCtx, 50*time.Millisecond)
			} else if !tc.parent {
				childCtx, cancelChild = context.WithCancel(parentCtx)
			}
			defer cancelChild()
			cc := isoChild(p, "extract:m")
			done := make(chan error, 1)
			go func() {
				s, e := (&auxiliaryClientForExecutor{executor: ex}).Stream(childCtx, cc)
				if e == nil {
					_, e = aipapi.Collect(childCtx, s)
				}
				done <- e
			}()
			<-opened
			if tc.parent {
				cancelParent()
			} else if !tc.timeout {
				cancelChild()
			}
			e = <-done
			if !errors.Is(e, tc.want) {
				t.Fatalf("error=%v want=%v", e, tc.want)
			}
			after, _ := ss.LoadByID(context.Background(), domain.SessionID(p.Session.AuthoritativeSessionID))
			if !after.ResumeEligible || after.ResumeFingerprint != before.ResumeFingerprint {
				t.Fatalf("parent continuity changed")
			}
		})
	}
}

func mustScope(ctx context.Context) scope.PrincipalScopeView {
	s, _ := scope.ScopeFromContext(ctx)
	return s
}
