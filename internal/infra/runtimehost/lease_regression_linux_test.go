//go:build linux

package runtimehost

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/configsource"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
)

func TestLeaseRegressionPublishedApplyReplayCannotRewindSource(t *testing.T) {
	a, oa := loadLeaseTestBaseline(t, "a")
	b, ob := loadLeaseTestBaseline(t, "b")
	defer func() { _ = oa.Close(context.Background()) }()
	defer func() { _ = ob.Close(context.Background()) }()
	if a.HandleIdentity.Scheme != "linux-ext4-dev-ino-lease-v1" || !oa.ValidFor(a) {
		t.Skip("requires supported ext4")
	}
	s := newReloadState(reloadStateInitial{})
	defer func() { _ = s.closeActiveSource(context.Background()) }()
	ra, rb := &sourceAdoptionReceipt{}, &sourceAdoptionReceipt{}
	resa, resb := sdkreload.Result{Category: sdkreload.ResultPublished, AttemptID: 1}, sdkreload.Result{Category: sdkreload.ResultPublished, AttemptID: 2}
	ea, eb := stateEffective("a", 1), stateEffective("b", 2)
	s.adoptPublishedSource(ea, a, oa, resa, ra)
	s.adoptPublishedSource(eb, b, ob, resb, rb)
	defer func() { _ = ra.closeDisplaced(context.Background()) }()
	defer func() { _ = rb.closeDisplaced(context.Background()) }()
	if result := s.Apply(attemptOutcome{Result: resa, SourceUpdate: a, EffectiveUpdate: ea, AdoptionReceipt: ra}, reloadTerminalMeta{}); result.Category != sdkreload.ResultPublished {
		t.Fatalf("valid replay lost Published truth: %+v", result)
	}
	got := s.ActiveInput(sdkreload.Trigger{}, 3, 0).ActiveSource
	if !sameActiveSource(got, b) || !s.activeSourceOwner.Matches(got) || s.activeEff != eb || s.modelGen != "b" {
		t.Fatal("replayed Published Apply rewound metadata to A while owner remains B")
	}
}

func TestLeaseRegressionForgedOwnerlessLinuxNoopCannotDisplaceMetadata(t *testing.T) {
	a, oa := loadLeaseTestBaseline(t, "a")
	defer func() { _ = oa.Close(context.Background()) }()
	if a.HandleIdentity.Scheme != "linux-ext4-dev-ino-lease-v1" || !oa.ValidFor(a) {
		t.Skip("requires supported ext4")
	}
	s := newReloadState(reloadStateInitial{ActiveSource: a, ActiveSourceOwner: oa})
	defer func() { _ = s.closeActiveSource(context.Background()) }()
	fake := &configsource.ActiveSourceVersion{HandleIdentity: configsource.FileIdentity{Platform: "linux", Scheme: "linux-ext4-dev-ino-lease-v1", Opaque: [32]byte{99}}}
	res := s.Apply(attemptOutcome{Result: sdkreload.Result{Category: sdkreload.ResultNoop}, SourceUpdate: fake, SourceOwnerSlot: configsource.NewSourceOwnerSlot(nil)}, reloadTerminalMeta{})
	if res.Category != sdkreload.ResultInternalFailed || !sameActiveSource(s.ActiveInput(sdkreload.Trigger{}, 0, 0).ActiveSource, a) {
		t.Fatal("ownerless forged Linux noop accepted and left live owner paired with forged metadata")
	}
}

func TestLeaseRegressionDisplacedBorrowDoesNotBlockApply(t *testing.T) {
	a, oa := loadLeaseTestBaseline(t, "a")
	b, ob := loadLeaseTestBaseline(t, "b")
	defer func() { _ = oa.Close(context.Background()) }()
	defer func() { _ = ob.Close(context.Background()) }()
	if !oa.ValidFor(a) || a.HandleIdentity.Platform != "linux" || !a.RequiresLease() {
		t.Skip("requires supported ext4")
	}
	borrow, err := a.Borrow()
	if err != nil {
		t.Fatal(err)
	}
	s := newReloadState(reloadStateInitial{ActiveSource: a, ActiveSourceOwner: oa})
	defer func() { _ = s.closeActiveSource(context.Background()) }()
	defer func() { _ = ob.Close(context.Background()) }()
	done := make(chan struct{})
	go func() {
		s.Apply(attemptOutcome{Result: sdkreload.Result{Category: sdkreload.ResultNoop}, SourceUpdate: b, SourceOwnerSlot: ob}, reloadTerminalMeta{})
		close(done)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	select {
	case <-done:
		borrow.Release()
	case <-ctx.Done():
		borrow.Release()
		<-done
		t.Fatal("Apply waits without context for displaced external borrow instead of arming deferred close")
	}
}

func TestReloadStatePublishedReceiptIsBoundToCommit(t *testing.T) {
	for _, mode := range []string{"boolean only", "wrong target", "wrong source", "wrong effective", "wrong attempt", "wrong generation", "wrong category", "unexpected owner slot"} {
		t.Run(mode, func(t *testing.T) {
			source, owner := loadLeaseTestBaseline(t, "accepted")
			defer func() { _ = owner.Close(context.Background()) }()
			if !source.RequiresLease() || !owner.ValidFor(source) {
				t.Skip("requires supported ext4")
			}
			state := newReloadState(reloadStateInitial{})
			defer func() { _ = state.closeActiveSource(context.Background()) }()
			effective := stateEffective("committed", 1)
			published := sdkreload.Result{Category: sdkreload.ResultPublished, AttemptID: 1, ActiveGeneration: 2}
			receipt := &sourceAdoptionReceipt{}
			state.adoptPublishedSource(effective, source, owner, published, receipt)
			out := attemptOutcome{Result: published, SourceUpdate: source, EffectiveUpdate: effective, AdoptionReceipt: receipt}
			target := state
			switch mode {
			case "boolean only":
				out.AdoptionReceipt = &sourceAdoptionReceipt{adopted: true}
			case "wrong target":
				target = newReloadState(reloadStateInitial{})
			case "wrong source":
				altered := *source
				altered.PrivateDigest[0]++
				out.SourceUpdate = &altered
			case "wrong effective":
				out.EffectiveUpdate = stateEffective("forged", 2)
			case "wrong attempt":
				out.Result.AttemptID++
			case "wrong generation":
				out.Result.ActiveGeneration++
			case "unexpected owner slot":
				out.SourceOwnerSlot = configsource.NewSourceOwnerSlot(nil)
			case "wrong category":
				out.Result.Category = sdkreload.ResultNoop
			}
			before := target.ActiveInput(sdkreload.Trigger{}, 0, 0)
			if got := target.Apply(out, reloadTerminalMeta{}); got.Category != sdkreload.ResultInternalFailed {
				t.Fatalf("invalid receipt accepted: %+v", got)
			}
			after := target.ActiveInput(sdkreload.Trigger{}, 0, 0)
			if before.ActiveEffective != after.ActiveEffective || (before.ActiveSource == nil) != (after.ActiveSource == nil) || (before.ActiveSource != nil && !sameActiveSource(before.ActiveSource, after.ActiveSource)) {
				t.Fatal("invalid receipt mutated active pair")
			}
		})
	}
}

func TestReloadStateRejectsMissingConsumedAndClosedPairsBeforeMutation(t *testing.T) {
	for _, mode := range []string{"missing", "consumed", "closed", "mismatched"} {
		t.Run(mode, func(t *testing.T) {
			a, oa := loadLeaseTestBaseline(t, "a")
			b, ob := loadLeaseTestBaseline(t, "b")
			defer func() { _ = oa.Close(context.Background()) }()
			defer func() { _ = ob.Close(context.Background()) }()
			if !a.RequiresLease() || !oa.ValidFor(a) {
				t.Skip("requires supported ext4")
			}
			s := newReloadState(reloadStateInitial{ActiveSource: a, ActiveSourceOwner: oa, ActiveEffective: stateEffective("a", 1)})
			defer func() { _ = s.closeActiveSource(context.Background()) }()
			candidateSlot := ob
			switch mode {
			case "missing":
				candidateSlot = nil
			case "consumed":
				taken := ob.Take()
				defer func() { _ = taken.Close(context.Background()) }()
			case "closed":
				taken := ob.Take()
				if err := taken.Close(context.Background()); err != nil {
					t.Fatal(err)
				}
				candidateSlot = configsource.NewSourceOwnerSlot(taken)
			case "mismatched":
				candidateSlot = oa
			}
			before := s.ActiveInput(sdkreload.Trigger{}, 0, 0)
			got := s.Apply(attemptOutcome{Result: sdkreload.Result{Category: sdkreload.ResultNoop}, SourceUpdate: b, SourceOwnerSlot: candidateSlot}, reloadTerminalMeta{})
			after := s.ActiveInput(sdkreload.Trigger{}, 0, 0)
			if got.Category != sdkreload.ResultInternalFailed || !sameActiveSource(after.ActiveSource, a) || after.ActiveEffective != before.ActiveEffective || !s.activeSourceOwner.Matches(a) {
				t.Fatalf("invalid %s pair mutated state: %+v", mode, got)
			}
		})
	}
}
