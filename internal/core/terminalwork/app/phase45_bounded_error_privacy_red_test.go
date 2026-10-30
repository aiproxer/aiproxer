package app_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/terminalwork"
	"github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

func TestPhase45_ProcessorBoundedErrorMessageNeverPersistsRawInvokeError(t *testing.T) {
	t.Parallel()
	const secret = "INVOKE_SECRET_TOKEN=supersecret"
	clock := &manualClock{t: time.Date(2026, 7, 18, 10, 12, 0, 0, time.UTC)}
	store := openStore(t, clock)
	prov := &fakeProvider{
		id: "prov-a", kinds: []sdk.WorkKind{sdk.WorkKindSettleRequestProvider}, ver: "1",
		fn: func(context.Context, terminalwork.WorkRecord, string) error {
			return errors.New("provider boom: " + secret)
		},
	}
	reg := app.NewRegistry()
	_ = reg.Register(prov)
	seedPending(t, store, clock, sampleRec("w-priv", "sk-priv", "prov-a", sdk.WorkKindSettleRequestProvider))
	p := newProc(t, store, reg, clock, app.Config{})
	_ = p.ProcessDue(context.Background())
	got, err := store.GetByWorkID(context.Background(), "w-priv")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != sdk.WorkStateRetry {
		t.Fatalf("state=%q want retry", got.State)
	}
	if got.Error.Code == "" {
		t.Fatal("expected classified error code")
	}
	if strings.Contains(got.Error.Message, secret) || strings.Contains(got.Error.Message, "provider boom") {
		t.Fatalf("BoundedError.Message leaked raw invoke error: %q", got.Error.Message)
	}
	if got.Error.Message == "" {
		t.Fatal("expected fixed safe message")
	}
}
