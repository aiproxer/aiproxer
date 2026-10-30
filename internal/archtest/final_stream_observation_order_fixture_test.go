package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// These fixtures exercise the dispatcher-scoped observation-order validator on
// miniature parsed sources. They assert the validator's VERDICT, not merely that
// a fixture parses: a positive must pass and each negative must fail for its own
// intended defect.
//
// The real source of the historical false positive is a sibling closure. The
// approved private pending-completion drain is defined in Recv and already runs
// the mandatory recorder and fail-closed final observer during preflight before
// installation/activation; at release it passes recorded/finalObserved so the
// observation cannot repeat. A shared recursive walker compared that sibling's
// already-preflighted observation against the ordinary dispatch's own
// transform/gate/authority statements. Defining a closure does not execute its
// body, so the correct coordinator-scoped rule is what these fixtures pin.
func fixtureTransformObserveSpec() recvDispatchSpec {
	return recvDispatchSpec{owner: dispatchClientFacingEventName, calls: []string{"transformClientEvent", "observeClientFacing"}}
}

func fixtureGateObserveSpec() recvDispatchSpec {
	return recvDispatchSpec{owner: dispatchClientFacingEventName, calls: []string{"applyCompletionGates", "observeClientFacing"}}
}

func fixtureFinalizeObserveSpec() recvDispatchSpec {
	return recvDispatchSpec{owner: dispatchClientFacingEventName, calls: []string{"finalizeResponseFinishedAuthority", "observeClientFacing"}}
}

// parseFixtureRecv parses a miniature Recv source and returns its declaration.
func parseFixtureRecv(t *testing.T, src string) *ast.FuncDecl {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "fixture.go", "package fixture\n"+src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	fn := findFunc(f, "Recv")
	if fn == nil {
		t.Fatal("fixture Recv not found")
	}
	return fn
}

// privateDrainSibling is a correctly preflighted private drain that records and
// final-observes once, then releases without repeating either effect. It is
// definitionally correct and must never influence the ordinary dispatch verdict,
// whether it is defined before or after the dispatch closure.
const privateDrainSibling = `
	drainPending := func(expected *pendingCompletion, origin *attemptSession) (aipapi.Event, bool, error) {
		out, _, err := p.observeClientFacing(ctx, ev, responseEventInput{recorded: true, finalObserved: true})
		if !pendingExpectedFence(expected, origin) {
			return aipapi.Event{}, false, err
		}
		return out, true, err
	}
`

const correctDispatchBody = `
	dispatchClientFacingEvent := func(ev aipapi.Event, prepared recvEventPreparation) (aipapi.Event, bool, error) {
		transformed := p.transformClientEvent(ctx, facts, attempt, ev, prepared)
		gated := p.applyCompletionGates(ctx, transformed.gates, facts, attempt, ev, terminal.committed())
		usageEv, ok, err := terminal.finalizeResponseFinishedAuthority(ctx, gated.event, facts.terminalFacts(), attempt, p, hooks)
		out, recording, err2 := p.observeClientFacing(ctx, gated.event, responseEventInput{})
		return out, ok || recording, errors.Join(err, err2)
	}
`

// TestRecvDispatchCoordinatorScope_positivesSatisfyValidator proves a correctly
// ordered coordinator passes for every ratchet spec, with the private drain
// sibling defined on either side of it and with nested decoy closures inside it.
func TestRecvDispatchCoordinatorScope_positivesSatisfyValidator(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		src  string
	}{
		{name: "private drain defined before dispatch", src: privateDrainSibling + correctDispatchBody},
		{name: "private drain defined after dispatch", src: correctDispatchBody + privateDrainSibling},
		{name: "nested decoy duplicates calls beside correct own order", src: `
			dispatchClientFacingEvent := func(ev aipapi.Event, prepared recvEventPreparation) (aipapi.Event, bool, error) {
				transformed := p.transformClientEvent(ctx, facts, attempt, ev, prepared)
				nested := func() error {
					deep := func() error {
						gated := p.applyCompletionGates(ctx, transformed.gates, facts, attempt, ev, terminal.committed())
						usageEv, ok, err := terminal.finalizeResponseFinishedAuthority(ctx, gated.event, facts.terminalFacts(), attempt, p, hooks)
						_, _, _ = usageEv, ok, err
						_, _ = gated, err
						return nil
					}
					return deep()
				}
				if err := nested(); err != nil {
					return aipapi.Event{}, false, err
				}
				gated := p.applyCompletionGates(ctx, transformed.gates, facts, attempt, transformed.event, terminal.committed())
				usageEv, ok, err := terminal.finalizeResponseFinishedAuthority(ctx, gated.event, facts.terminalFacts(), attempt, p, hooks)
				_, _, _ = usageEv, ok, err
				out, _, err := p.observeClientFacing(ctx, gated.event, responseEventInput{})
				return out, false, err
			}
		`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fn := parseFixtureRecv(t, "func Recv(ctx context.Context) (aipapi.Event, error) {\n"+tc.src+"\n\treturn aipapi.Event{}, nil\n}\n")
			for _, spec := range []recvDispatchSpec{fixtureTransformObserveSpec(), fixtureGateObserveSpec(), fixtureFinalizeObserveSpec()} {
				if err := validateRecvDispatchOrder(fn, spec); err != nil {
					t.Fatalf("%s: a correctly ordered coordinator must pass: %v", spec.owner, err)
				}
			}
		})
	}
}

// TestRecvDispatchCoordinatorScope_negativesFailForTheirOwnDefect proves each
// defect is still caught: a misordered actual dispatch can never be hidden by a
// correctly ordered sibling or nested decoy, and a missing, duplicated, or
// non-literal coordinator is an explicit failure.
func TestRecvDispatchCoordinatorScope_negativesFailForTheirOwnDefect(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		src     string
		spec    recvDispatchSpec
		wantSub string
	}{
		{
			name: "actual dispatch observes before transform",
			src: `
				dispatchClientFacingEvent := func(ev aipapi.Event, prepared recvEventPreparation) (aipapi.Event, bool, error) {
					out, _, err := p.observeClientFacing(ctx, ev, responseEventInput{})
					transformed := p.transformClientEvent(ctx, facts, attempt, ev, prepared)
					return out, false, errors.Join(err, transformed.err)
				}
			` + privateDrainSibling,
			spec:    fixtureTransformObserveSpec(),
			wantSub: "must call transformClientEvent before observeClientFacing",
		},
		{
			name: "actual dispatch observes before gate resolution",
			src: `
				dispatchClientFacingEvent := func(ev aipapi.Event, prepared recvEventPreparation) (aipapi.Event, bool, error) {
					out, _, err := p.observeClientFacing(ctx, ev, responseEventInput{})
					gated := p.applyCompletionGates(ctx, transformed.gates, facts, attempt, ev, terminal.committed())
					return out, false, errors.Join(err, gated.err)
				}
			`,
			spec:    fixtureGateObserveSpec(),
			wantSub: "must call applyCompletionGates before observeClientFacing",
		},
		{
			name: "actual dispatch observes before finish authority",
			src: `
				dispatchClientFacingEvent := func(ev aipapi.Event, prepared recvEventPreparation) (aipapi.Event, bool, error) {
					out, _, err := p.observeClientFacing(ctx, ev, responseEventInput{})
					usageEv, ok, ferr := terminal.finalizeResponseFinishedAuthority(ctx, ev, facts.terminalFacts(), attempt, p, hooks)
					return out, ok, errors.Join(err, ferr)
				}
			`,
			spec:    fixtureFinalizeObserveSpec(),
			wantSub: "must call finalizeResponseFinishedAuthority before observeClientFacing",
		},
		{
			name: "omitted transform behind a correctly ordered sibling decoy",
			src: `
				dispatchClientFacingEvent := func(ev aipapi.Event, prepared recvEventPreparation) (aipapi.Event, bool, error) {
					gated := p.applyCompletionGates(ctx, ev.Gates, facts, attempt, ev, terminal.committed())
					usageEv, ok, err := terminal.finalizeResponseFinishedAuthority(ctx, gated.event, facts.terminalFacts(), attempt, p, hooks)
					_, _, _ = usageEv, ok, err
					out, _, err2 := p.observeClientFacing(ctx, gated.event, responseEventInput{})
					return out, false, errors.Join(err, err2)
				}
			` + `
				sibling := func() error {
					transformed := p.transformClientEvent(ctx, facts, attempt, ev, prepared)
					gated := p.applyCompletionGates(ctx, transformed.gates, facts, attempt, ev, terminal.committed())
					usageEv, ok, err := terminal.finalizeResponseFinishedAuthority(ctx, gated.event, facts.terminalFacts(), attempt, p, hooks)
					_, _, _ = usageEv, ok, err
					_, _, _ = p.observeClientFacing(ctx, gated.event, responseEventInput{})
					return err
				}
				_ = sibling
			`,
			spec:    fixtureTransformObserveSpec(),
			wantSub: "must call transformClientEvent inside the dispatchClientFacingEvent coordinator",
		},
		{
			name: "omitted gate resolution behind a correctly ordered nested decoy",
			src: `
				dispatchClientFacingEvent := func(ev aipapi.Event, prepared recvEventPreparation) (aipapi.Event, bool, error) {
					nested := func() error {
						transformed := p.transformClientEvent(ctx, facts, attempt, ev, prepared)
						gated := p.applyCompletionGates(ctx, transformed.gates, facts, attempt, ev, terminal.committed())
						usageEv, ok, err := terminal.finalizeResponseFinishedAuthority(ctx, gated.event, facts.terminalFacts(), attempt, p, hooks)
						_, _, _ = usageEv, ok, err
						_, _, _ = p.observeClientFacing(ctx, gated.event, responseEventInput{})
						return err
					}
					_ = nested
					transformed := p.transformClientEvent(ctx, facts, attempt, ev, prepared)
					usageEv, ok, err := terminal.finalizeResponseFinishedAuthority(ctx, transformed.event, facts.terminalFacts(), attempt, p, hooks)
					_, _, _ = usageEv, ok, err
					out, _, err2 := p.observeClientFacing(ctx, transformed.event, responseEventInput{})
					return out, false, errors.Join(err, err2)
				}
			`,
			spec:    fixtureGateObserveSpec(),
			wantSub: "must call applyCompletionGates inside the dispatchClientFacingEvent coordinator",
		},
		{
			name: "omitted finish authority behind a correctly ordered sibling decoy",
			src: `
				dispatchClientFacingEvent := func(ev aipapi.Event, prepared recvEventPreparation) (aipapi.Event, bool, error) {
					transformed := p.transformClientEvent(ctx, facts, attempt, ev, prepared)
					gated := p.applyCompletionGates(ctx, transformed.gates, facts, attempt, ev, terminal.committed())
					out, _, err := p.observeClientFacing(ctx, gated.event, responseEventInput{})
					return out, false, err
				}
			` + `
				sibling := func() error {
					transformed := p.transformClientEvent(ctx, facts, attempt, ev, prepared)
					gated := p.applyCompletionGates(ctx, transformed.gates, facts, attempt, ev, terminal.committed())
					usageEv, ok, err := terminal.finalizeResponseFinishedAuthority(ctx, gated.event, facts.terminalFacts(), attempt, p, hooks)
					_, _, _ = usageEv, ok, err
					_, _, _ = p.observeClientFacing(ctx, gated.event, responseEventInput{})
					return err
				}
				_ = sibling
			`,
			spec:    fixtureFinalizeObserveSpec(),
			wantSub: "must call finalizeResponseFinishedAuthority inside the dispatchClientFacingEvent coordinator",
		},
		{
			name: "nested decoy cannot satisfy a missing required call",
			src: `
				dispatchClientFacingEvent := func(ev aipapi.Event, prepared recvEventPreparation) (aipapi.Event, bool, error) {
					nested := func() error {
						_, _, err := p.observeClientFacing(ctx, ev, responseEventInput{})
						return err
					}
					if err := nested(); err != nil {
						return aipapi.Event{}, false, err
					}
					usageEv, ok, err := terminal.finalizeResponseFinishedAuthority(ctx, ev, facts.terminalFacts(), attempt, p, hooks)
					return usageEv, ok, err
				}
			`,
			spec:    fixtureFinalizeObserveSpec(),
			wantSub: "must call observeClientFacing inside the dispatchClientFacingEvent coordinator",
		},
		{
			name: "omitted required call",
			src: `
				dispatchClientFacingEvent := func(ev aipapi.Event, prepared recvEventPreparation) (aipapi.Event, bool, error) {
					transformed := p.transformClientEvent(ctx, facts, attempt, ev, prepared)
					return transformed.event, false, nil
				}
			`,
			spec:    fixtureTransformObserveSpec(),
			wantSub: "must call observeClientFacing inside the dispatchClientFacingEvent coordinator",
		},
		{
			name:    "missing coordinator",
			src:     privateDrainSibling,
			spec:    fixtureTransformObserveSpec(),
			wantSub: "Recv must define dispatchClientFacingEvent exactly once",
		},
		{
			name: "duplicate coordinator",
			src: correctDispatchBody + `
				dispatchClientFacingEvent = func(ev aipapi.Event, prepared recvEventPreparation) (aipapi.Event, bool, error) {
					out, _, err := p.observeClientFacing(ctx, ev, responseEventInput{})
					return out, false, err
				}
			`,
			spec:    fixtureTransformObserveSpec(),
			wantSub: "want exactly one coordinator",
		},
		{
			name: "non literal coordinator",
			src: `
				var dispatchClientFacingEvent = someOtherFunc
			`,
			spec:    fixtureTransformObserveSpec(),
			wantSub: "must be assigned a function literal",
		},
		{
			name: "singleton non literal assignment coordinator",
			src: `
				dispatchClientFacingEvent = someOtherFunc
			`,
			spec:    fixtureTransformObserveSpec(),
			wantSub: "must be assigned a function literal",
		},
		{
			name: "const non literal coordinator",
			// Syntax-valid for this parse-only validator: the fixture is never type
			// checked, and a const binding of the coordinator name is still a
			// definition that is not the required top-level literal assignment.
			src: `
				const dispatchClientFacingEvent = someOtherFunc
			`,
			spec:    fixtureTransformObserveSpec(),
			wantSub: "must be assigned a function literal",
		},
		{
			name: "multi target non literal replacement of a valid literal",
			src: correctDispatchBody + `
				replacement := func(ev aipapi.Event, prepared recvEventPreparation) (aipapi.Event, bool, error) {
					out, _, err := p.observeClientFacing(ctx, ev, responseEventInput{})
					return out, false, err
				}
				unused := 0
				dispatchClientFacingEvent, unused = replacement, unused
			`,
			spec:    fixtureTransformObserveSpec(),
			wantSub: "must be assigned a function literal",
		},
		{
			name: "multi target literal reassignment of a valid literal",
			src: correctDispatchBody + `
				unused := 0
				dispatchClientFacingEvent, unused = func(ev aipapi.Event, prepared recvEventPreparation) (aipapi.Event, bool, error) {
					out, _, err := p.observeClientFacing(ctx, ev, responseEventInput{})
					return out, false, err
				}, unused
			`,
			spec:    fixtureTransformObserveSpec(),
			wantSub: "must be assigned a function literal",
		},
		{
			name: "multi target non literal replacement with coordinator in second position",
			src: correctDispatchBody + `
				replacement := func(ev aipapi.Event, prepared recvEventPreparation) (aipapi.Event, bool, error) {
					out, _, err := p.observeClientFacing(ctx, ev, responseEventInput{})
					return out, false, err
				}
				unused := 0
				unused, dispatchClientFacingEvent = unused, replacement
			`,
			spec:    fixtureTransformObserveSpec(),
			wantSub: "must be assigned a function literal",
		},
		{
			name: "multi target literal reassignment with coordinator in second position",
			src: correctDispatchBody + `
				unused := 0
				unused, dispatchClientFacingEvent = unused, func(ev aipapi.Event, prepared recvEventPreparation) (aipapi.Event, bool, error) {
					out, _, err := p.observeClientFacing(ctx, ev, responseEventInput{})
					return out, false, err
				}
			`,
			spec:    fixtureTransformObserveSpec(),
			wantSub: "must be assigned a function literal",
		},
		{
			name: "multi target coordinator definition alone",
			src: `
				other := func() error { return nil }
				unused := 0
				dispatchClientFacingEvent, unused = other, unused
			`,
			spec:    fixtureTransformObserveSpec(),
			wantSub: "must be assigned a function literal",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fn := parseFixtureRecv(t, "func Recv(ctx context.Context) (aipapi.Event, error) {\n"+tc.src+"\n\treturn aipapi.Event{}, nil\n}\n")
			err := validateRecvDispatchOrder(fn, tc.spec)
			if err == nil {
				t.Fatalf("validator must reject %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("validator rejected %s for the wrong reason: %v (want %q)", tc.name, err, tc.wantSub)
			}
		})
	}
}
