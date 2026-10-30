package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/terminalwork"
	"github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
	"github.com/aiproxer/aiproxer/internal/infra/terminalwork/workstore"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/genpin"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

type permanentProv struct {
	id string
}

func (p permanentProv) ProviderID() string { return p.id }
func (p permanentProv) Version() string    { return "1" }
func (p permanentProv) SupportedKinds() []sdk.WorkKind {
	return []sdk.WorkKind{sdk.WorkKindSettleRequestProvider}
}

func (p permanentProv) Invoke(context.Context, terminalwork.WorkRecord, string) error {
	return errors.Join(app.ErrMalformedProvider, errors.New("permanent"))
}

func TestGenerationPin_Finalize_QuarantineReleasesExactlyOnce(t *testing.T) {
	t.Parallel()
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "genpin-finalize"})
	if err != nil {
		t.Fatal(err)
	}
	pins := app.NewGenerationPinTracker()
	pin := &countingPin{}
	ctx := genpin.WithRetainer(context.Background(), &staticRetainer{inst: "inst-a", id: "11", pin: pin})
	svc := app.NewIntentService(store, app.IntentServiceConfig{Pins: pins})
	if err := svc.AcceptSettleFailure(ctx, app.SettleFailureInput{
		RequestID:  "req-fin",
		AttemptID:  "a",
		ProviderID: "quota",
		Handles:    []string{"h"},
		Versions:   terminalwork.BoundVersions{GenerationID: "3", ProviderID: "quota"},
	}); err != nil {
		t.Fatal(err)
	}
	if pins.Len() != 1 {
		t.Fatalf("pins=%d", pins.Len())
	}
	reg := app.NewRegistry()
	prov := permanentProv{id: "quota"}
	if err := reg.Register(prov); err != nil {
		t.Fatal(err)
	}
	proc, err := app.NewProcessor(store, reg, app.Config{
		OwnerID:        "o",
		ClaimTTL:       time.Minute,
		ClaimLimit:     4,
		GenerationPins: pins,
		GenerationResolver: &mapGenerationResolver{byGen: map[string]map[string]app.EffectProvider{
			"inst-a/11": {"quota": prov},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = proc.ProcessDue(context.Background())
	if pins.Len() != 0 {
		t.Fatalf("pins after quarantine=%d", pins.Len())
	}
	if pin.releases.Load() != 1 {
		t.Fatalf("releases=%d want 1", pin.releases.Load())
	}
}
