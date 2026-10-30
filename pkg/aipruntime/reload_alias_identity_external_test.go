package aipruntime_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipruntime"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
)

// Task 2.3: every supported public reload name must be an exact alias of the
// canonical SDK contract — assignment identity with no conversion.
func TestReloadPublicAliases_AssignmentIdentityWithSDK(t *testing.T) {
	t.Parallel()

	acceptSDKTriggerKind := func(sdkreload.TriggerKind) {}
	acceptPublicTriggerKind := func(aipruntime.TriggerKind) {}
	acceptSDKResultCategory := func(sdkreload.ResultCategory) {}
	acceptPublicResultCategory := func(aipruntime.ResultCategory) {}
	acceptSDKTrigger := func(sdkreload.Trigger) {}
	acceptPublicTrigger := func(aipruntime.ReloadTrigger) {}
	acceptSDKResult := func(sdkreload.Result) {}
	acceptPublicResult := func(aipruntime.ReloadResult) {}
	acceptSDKStatus := func(sdkreload.Status) {}
	acceptPublicStatus := func(aipruntime.ReloadStatus) {}
	acceptSDKHistory := func(sdkreload.HistoryEntry) {}
	acceptPublicHistory := func(aipruntime.HistoryEntry) {}

	acceptSDKTriggerKind(aipruntime.TriggerKind(""))
	acceptPublicTriggerKind(sdkreload.TriggerKind(""))
	acceptSDKResultCategory(aipruntime.ResultCategory(""))
	acceptPublicResultCategory(sdkreload.ResultCategory(""))
	acceptSDKTrigger(aipruntime.ReloadTrigger{})
	acceptPublicTrigger(sdkreload.Trigger{})
	acceptSDKResult(aipruntime.ReloadResult{})
	acceptPublicResult(sdkreload.Result{})
	acceptSDKStatus(aipruntime.ReloadStatus{})
	acceptPublicStatus(sdkreload.Status{})
	acceptSDKHistory(aipruntime.HistoryEntry{})
	acceptPublicHistory(sdkreload.HistoryEntry{})

	cases := []struct {
		name string
		pub  any
		sdk  any
	}{
		{"TriggerKind", aipruntime.TriggerKind(""), sdkreload.TriggerKind("")},
		{"ResultCategory", aipruntime.ResultCategory(""), sdkreload.ResultCategory("")},
		{"ReloadTrigger", aipruntime.ReloadTrigger{}, sdkreload.Trigger{}},
		{"ReloadResult", aipruntime.ReloadResult{}, sdkreload.Result{}},
		{"ReloadStatus", aipruntime.ReloadStatus{}, sdkreload.Status{}},
		{"HistoryEntry", aipruntime.HistoryEntry{}, sdkreload.HistoryEntry{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pt, st := reflect.TypeOf(tc.pub), reflect.TypeOf(tc.sdk)
			if pt != st {
				t.Fatalf("%s public type %v is not identical to SDK type %v (want exact alias)", tc.name, pt, st)
			}
		})
	}
}

func TestReloadPublicAliases_ConstantReexportsMatchSDK(t *testing.T) {
	t.Parallel()
	consts := []struct {
		name string
		pub  any
		sdk  any
	}{
		{"TriggerSIGHUP", aipruntime.TriggerSIGHUP, sdkreload.TriggerSIGHUP},
		{"TriggerAPI", aipruntime.TriggerAPI, sdkreload.TriggerAPI},
		{"ResultPublished", aipruntime.ResultPublished, sdkreload.ResultPublished},
		{"ResultNoop", aipruntime.ResultNoop, sdkreload.ResultNoop},
		{"ResultBusy", aipruntime.ResultBusy, sdkreload.ResultBusy},
		{"ResultRestartRequired", aipruntime.ResultRestartRequired, sdkreload.ResultRestartRequired},
		{"ResultRetentionBlocked", aipruntime.ResultRetentionBlocked, sdkreload.ResultRetentionBlocked},
		{"ResultInvalid", aipruntime.ResultInvalid, sdkreload.ResultInvalid},
		{"ResultSourceIntegrity", aipruntime.ResultSourceIntegrity, sdkreload.ResultSourceIntegrity},
		{"ResultCanceled", aipruntime.ResultCanceled, sdkreload.ResultCanceled},
		{"ResultPreparationFailed", aipruntime.ResultPreparationFailed, sdkreload.ResultPreparationFailed},
		{"ResultInternalFailed", aipruntime.ResultInternalFailed, sdkreload.ResultInternalFailed},
	}
	for _, tc := range consts {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if !reflect.DeepEqual(tc.pub, tc.sdk) {
				t.Fatalf("%s pub=%v sdk=%v", tc.name, tc.pub, tc.sdk)
			}
			if reflect.TypeOf(tc.pub) != reflect.TypeOf(tc.sdk) {
				t.Fatalf("%s type drift pub=%v sdk=%v", tc.name, reflect.TypeOf(tc.pub), reflect.TypeOf(tc.sdk))
			}
		})
	}
	pub := aipruntime.ResultCategories()
	sdk := sdkreload.ResultCategories()
	if len(pub) == 0 || len(pub) != len(sdk) {
		t.Fatal("ResultCategories must re-export the canonical vocabulary length")
	}
	if !reflect.DeepEqual(pub, sdk) {
		t.Fatalf("ResultCategories pub=%v sdk=%v", pub, sdk)
	}
	// The alias must preserve defensive-copy semantics: mutating the public result
	// must not leak into the SDK accessor.
	pub[0] = "decoy"
	if sdkreload.ResultCategories()[0] == "decoy" {
		t.Fatal("ResultCategories must return an independent copy per call")
	}
}

func TestReloadFacade_NilContextReturnsInternalFailed(t *testing.T) {
	t.Parallel()
	ctrl := aipruntime.NewReloadControlForTest(&fakeReloadQuery{
		reload: func(context.Context, sdkreload.Trigger) sdkreload.Result {
			t.Fatal("nil context must not reach coordinator")
			return sdkreload.Result{}
		},
	})
	got := ctrl.Reload(nil, aipruntime.ReloadTrigger{Kind: aipruntime.TriggerAPI}) //nolint:staticcheck // intentional nil ctx
	if got.Category != aipruntime.ResultInternalFailed || got.ReasonCategory != "nil-context" {
		t.Fatalf("got=%+v", got)
	}
}

func TestReloadFacade_UnavailableControlReturnsInternalFailed(t *testing.T) {
	t.Parallel()
	var ctrl *aipruntime.ReloadControl
	got := ctrl.Reload(context.Background(), aipruntime.ReloadTrigger{Kind: aipruntime.TriggerAPI})
	if got.Category != aipruntime.ResultInternalFailed || got.ReasonCategory != "reload-unavailable" {
		t.Fatalf("got=%+v", got)
	}
	if st := ctrl.Status(); st.Busy {
		t.Fatal("nil control must not report busy")
	}
}

func TestReloadFacade_NoConversionRoundTrip(t *testing.T) {
	t.Parallel()
	accepted := time.Unix(42, 0).UTC()
	var saw sdkreload.Trigger
	ctrl := aipruntime.NewReloadControlForTest(&fakeReloadQuery{
		reload: func(_ context.Context, trigger sdkreload.Trigger) sdkreload.Result {
			saw = trigger
			return sdkreload.Result{
				Category:         sdkreload.ResultPublished,
				AttemptID:        7,
				ActiveGeneration: 3,
				RestartFields:    []string{"server.address"},
			}
		},
	})
	in := aipruntime.ReloadTrigger{Kind: aipruntime.TriggerAPI, AcceptedAt: accepted, SafeActor: "actor"}
	got := ctrl.Reload(context.Background(), in)
	if saw.Kind != sdkreload.TriggerAPI || saw.AcceptedAt != accepted || saw.SafeActor != "actor" {
		t.Fatalf("coordinator saw %+v", saw)
	}
	// Result is the same underlying type — no mapper conversion required.
	asSDK := func(v sdkreload.Result) sdkreload.Result { return v }(got)
	if asSDK.Category != sdkreload.ResultPublished || asSDK.AttemptID != 7 {
		t.Fatalf("result=%+v", asSDK)
	}
}
