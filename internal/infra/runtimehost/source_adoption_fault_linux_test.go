//go:build linux && configsource_faulttest

package runtimehost

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/configsource"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
)

type sourceAdoptionCommitPlane struct {
	startErr   error
	startPanic bool
	starts     atomic.Int32
	quiesces   atomic.Int32
	closes     atomic.Int32
}

func (*sourceAdoptionCommitPlane) Handler() http.Handler {
	return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
}

func (p *sourceAdoptionCommitPlane) StartPublished(context.Context) error {
	p.starts.Add(1)
	if p.startPanic {
		panic("private post-swap start failure")
	}
	return p.startErr
}

func (p *sourceAdoptionCommitPlane) Quiesce(context.Context) error {
	p.quiesces.Add(1)
	return nil
}

func (p *sourceAdoptionCommitPlane) Close() error {
	p.closes.Add(1)
	return nil
}

func TestCoordinator_PublishedStartFailureRetainsAdoptedSourceAndCanQuiesce(t *testing.T) {
	for _, tc := range []struct {
		name       string
		startErr   error
		startPanic bool
	}{
		{name: "returned error", startErr: errors.New("private post-swap start failure")},
		{name: "panic", startPanic: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			initialPath := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(initialPath, []byte("version: accepted\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			injectedCloseErr := errors.New("private displaced-source close failure")
			var displacedCloses atomic.Int32
			activeSource, ownerSlot, source, err := configsource.LoadCloseFaultBaseline(initialPath, func(f *os.File) error {
				displacedCloses.Add(1)
				return errors.Join(f.Close(), injectedCloseErr)
			})
			if err != nil {
				t.Fatal(err)
			}
			if activeSource.HandleIdentity.Scheme != "linux-ext4-dev-ino-lease-v1" {
				_ = ownerSlot.Close(context.Background())
				t.Fatal("fault-tagged coordinator test requires a supported ext4 source")
			}
			defer ownerSlot.Close(context.Background())

			replacementPath := filepath.Join(filepath.Dir(initialPath), "candidate.yaml")
			if err := os.WriteFile(replacementPath, []byte("version: candidate\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(replacementPath, initialPath); err != nil {
				t.Fatal(err)
			}

			manager := NewManager(8, nil)
			startup := manager.PrepareRequestPlane("startup", newRunnerFakePlane(nil))
			if err := manager.Publish(startup); err != nil {
				t.Fatal(err)
			}
			candidateEffective := runnerBaseEffective("fp-candidate", 2)
			var candidatePlane *sourceAdoptionCommitPlane
			var compileCalls atomic.Int32
			coordinator, err := NewCoordinator(CoordinatorDeps{
				Source: source,
				Loader: FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) {
					return candidateEffective, nil
				}),
				Compile: FuncCompiler(func(context.Context, *config.Config, map[string]int) (PublishedRequestPlane, error) {
					compileCalls.Add(1)
					candidatePlane = &sourceAdoptionCommitPlane{startErr: tc.startErr, startPanic: tc.startPanic}
					return candidatePlane, nil
				}),
				Manager: manager, Timeout: time.Second,
				ActiveEffective: runnerBaseEffective("fp-active", 1),
				ActiveSource:    activeSource, ActiveSourceOwner: ownerSlot,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !ownerSlot.IsEmpty() {
				t.Fatal("coordinator construction did not consume the startup owner slot")
			}

			published := coordinator.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
			if published.Category != sdkreload.ResultPublished || published.ActiveGeneration != 2 {
				t.Fatalf("post-adoption start failure changed committed result: %+v", published)
			}
			if manager.Active() == nil || manager.Active().ID() != 2 {
				t.Fatalf("post-adoption start failure changed active generation: %v", manager.Active())
			}
			if status := coordinator.Status(); status.LastResult.Category != sdkreload.ResultPublished || status.LastSuccess.Category != sdkreload.ResultPublished {
				t.Fatalf("coordinator status lost Published truth: %+v", status)
			}
			if candidatePlane == nil || candidatePlane.starts.Load() != 1 {
				t.Fatalf("StartPublished calls=%v want once", candidatePlane)
			}
			coordinator.state.mu.Lock()
			adoptedSource := cloneActiveSource(coordinator.state.activeSource)
			adoptedEffective := coordinator.state.activeEff
			pairMatches := coordinator.state.activeSourceOwner.ValidFor(coordinator.state.activeSource)
			coordinator.state.mu.Unlock()
			if adoptedSource == nil || adoptedSource.HandleIdentity == activeSource.HandleIdentity ||
				adoptedEffective != candidateEffective || !pairMatches {
				t.Fatalf("committed source/effective/owner pair diverged: source=%+v effective=%p pairMatches=%t", adoptedSource, adoptedEffective, pairMatches)
			}
			if displacedCloses.Load() != 1 {
				t.Fatalf("displaced source close calls=%d want once despite its injected failure", displacedCloses.Load())
			}

			noop := coordinator.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
			if noop.Category != sdkreload.ResultNoop || noop.ActiveGeneration != 2 {
				t.Fatalf("repeat read of the committed source did not remain a no-op: %+v", noop)
			}
			if compileCalls.Load() != 1 || candidatePlane.starts.Load() != 1 {
				t.Fatalf("post-swap work retried: compiles=%d starts=%d", compileCalls.Load(), candidatePlane.starts.Load())
			}

			coordinator.BeginShutdown()
			closeCtx, cancelClose := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancelClose()
			if err := coordinator.WaitForIdle(closeCtx); err != nil {
				t.Fatalf("source finalization after commit: %v", err)
			}
			if err := manager.ShutdownDetached(closeCtx); err != nil {
				t.Fatalf("quiesce/close after post-adoption failure: %v", err)
			}
			if !coordinator.state.activeSourceOwner.IsEmpty() {
				t.Fatal("shutdown retained the committed source owner")
			}
			if candidatePlane.quiesces.Load() != 1 || candidatePlane.closes.Load() != 1 {
				t.Fatalf("committed generation cleanup counts: quiesce=%d close=%d", candidatePlane.quiesces.Load(), candidatePlane.closes.Load())
			}
		})
	}
}
