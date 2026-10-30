package aipruntime_test

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipruntime"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
)

// fakeReloadQuery mirrors runtimehost.Coordinator Reload/Status for facade tests.
type fakeReloadQuery struct {
	mu     sync.Mutex
	busy   bool
	status sdkreload.Status
	reload func(ctx context.Context, trigger sdkreload.Trigger) sdkreload.Result
}

func (f *fakeReloadQuery) Reload(ctx context.Context, trigger sdkreload.Trigger) sdkreload.Result {
	f.mu.Lock()
	fn := f.reload
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, trigger)
	}
	return sdkreload.Result{Category: sdkreload.ResultNoop, ActiveGeneration: 1}
}

func (f *fakeReloadQuery) Status() sdkreload.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.status
	st.Busy = f.busy
	return st
}

func TestReloadFacade_ImportablePublicSurface(t *testing.T) {
	t.Parallel()
	var (
		_ aipruntime.TriggerKind
		_ aipruntime.ResultCategory
		_ aipruntime.ReloadTrigger
		_ aipruntime.ReloadResult
		_ aipruntime.ReloadStatus
		_ aipruntime.HistoryEntry
		_ = aipruntime.TriggerAPI
		_ = aipruntime.TriggerSIGHUP
		_ = aipruntime.ResultPublished
		_ = aipruntime.ResultNoop
		_ = aipruntime.ResultBusy
		_ = aipruntime.ResultRestartRequired
		_ = aipruntime.ResultRetentionBlocked
		_ = aipruntime.ResultCanceled
	)
	ctrl := aipruntime.NewReloadControlForTest(&fakeReloadQuery{})
	if ctrl == nil {
		t.Fatal("expected ReloadControl")
	}
	_ = ctrl.Reload(context.Background(), aipruntime.ReloadTrigger{Kind: aipruntime.TriggerAPI})
	_ = ctrl.Status()
}

func TestReloadFacade_ExactCategoryMapping(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   sdkreload.ResultCategory
		want aipruntime.ResultCategory
	}{
		{"published", sdkreload.ResultPublished, aipruntime.ResultPublished},
		{"noop", sdkreload.ResultNoop, aipruntime.ResultNoop},
		{"busy", sdkreload.ResultBusy, aipruntime.ResultBusy},
		{"restart_required", sdkreload.ResultRestartRequired, aipruntime.ResultRestartRequired},
		{"retention_blocked", sdkreload.ResultRetentionBlocked, aipruntime.ResultRetentionBlocked},
		{"invalid", sdkreload.ResultInvalid, aipruntime.ResultInvalid},
		{"source_integrity", sdkreload.ResultSourceIntegrity, aipruntime.ResultSourceIntegrity},
		{"canceled", sdkreload.ResultCanceled, aipruntime.ResultCanceled},
		{"preparation_failed", sdkreload.ResultPreparationFailed, aipruntime.ResultPreparationFailed},
		{"internal_failed", sdkreload.ResultInternalFailed, aipruntime.ResultInternalFailed},
	}
	if len(cases) != len(sdkreload.ResultCategories()) {
		t.Fatalf("cases=%d ResultCategories=%d", len(cases), len(sdkreload.ResultCategories()))
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			q := &fakeReloadQuery{
				reload: func(context.Context, sdkreload.Trigger) sdkreload.Result {
					return sdkreload.Result{
						Category:           tc.in,
						AttemptID:          9,
						ActiveGeneration:   3,
						PreviousGeneration: 2,
						RestartFields:      []string{"server.address"},
						RestartFieldCount:  1,
						ReasonCategory:     "classify",
						CoalescedSignals:   2,
					}
				},
			}
			ctrl := aipruntime.NewReloadControlForTest(q)
			got := ctrl.Reload(context.Background(), aipruntime.ReloadTrigger{Kind: aipruntime.TriggerAPI})
			if got.Category != tc.want {
				t.Fatalf("category=%q want %q", got.Category, tc.want)
			}
			if got.AttemptID != 9 || got.ActiveGeneration != 3 || got.PreviousGeneration != 2 {
				t.Fatalf("ids=%+v", got)
			}
			if got.RestartFieldCount != 1 || len(got.RestartFields) != 1 || got.RestartFields[0] != "server.address" {
				t.Fatalf("restart fields=%v count=%d", got.RestartFields, got.RestartFieldCount)
			}
			if got.ReasonCategory != "classify" || got.CoalescedSignals != 2 {
				t.Fatalf("meta=%+v", got)
			}
		})
	}
}

func TestReloadFacade_PreservesBusyNoopRestartRetentionCanceled(t *testing.T) {
	t.Parallel()
	outcomes := []sdkreload.ResultCategory{
		sdkreload.ResultBusy,
		sdkreload.ResultNoop,
		sdkreload.ResultRestartRequired,
		sdkreload.ResultRetentionBlocked,
		sdkreload.ResultCanceled,
	}
	for _, cat := range outcomes {
		t.Run(string(cat), func(t *testing.T) {
			t.Parallel()
			q := &fakeReloadQuery{
				reload: func(context.Context, sdkreload.Trigger) sdkreload.Result {
					return sdkreload.Result{Category: cat, ActiveGeneration: 4}
				},
				status: sdkreload.Status{
					ActiveGeneration: 4,
					Busy:             cat == sdkreload.ResultBusy,
					LastResult:       sdkreload.Result{Category: cat, ActiveGeneration: 4},
				},
			}
			if cat == sdkreload.ResultBusy {
				q.busy = true
			}
			ctrl := aipruntime.NewReloadControlForTest(q)
			res := ctrl.Reload(context.Background(), aipruntime.ReloadTrigger{Kind: aipruntime.TriggerAPI})
			if string(res.Category) != string(cat) {
				t.Fatalf("reload category=%q want %q", res.Category, cat)
			}
			st := ctrl.Status()
			if string(st.LastResult.Category) != string(cat) {
				t.Fatalf("status last=%q want %q", st.LastResult.Category, cat)
			}
			if cat == sdkreload.ResultBusy && !st.Busy {
				t.Fatal("expected Busy status")
			}
		})
	}
}

func TestReloadFacade_ImmutableCopiedDTOs(t *testing.T) {
	t.Parallel()
	fields := []string{"server.address", "management.listen"}
	hist := []sdkreload.HistoryEntry{{
		AttemptID:         1,
		Trigger:           sdkreload.TriggerAPI,
		Category:          sdkreload.ResultRestartRequired,
		ActiveGeneration:  2,
		RestartFieldCount: 2,
		ReasonCategory:    "classify",
		SafeActor:         "mgmt",
		RecordedAt:        time.Unix(100, 0).UTC(),
	}}
	q := &fakeReloadQuery{
		reload: func(context.Context, sdkreload.Trigger) sdkreload.Result {
			return sdkreload.Result{
				Category:          sdkreload.ResultRestartRequired,
				RestartFields:     fields,
				RestartFieldCount: 2,
				ActiveGeneration:  2,
			}
		},
		status: sdkreload.Status{
			ActiveGeneration: 2,
			LastResult: sdkreload.Result{
				Category:          sdkreload.ResultRestartRequired,
				RestartFields:     fields,
				RestartFieldCount: 2,
			},
			History: hist,
		},
	}
	ctrl := aipruntime.NewReloadControlForTest(q)

	res := ctrl.Reload(context.Background(), aipruntime.ReloadTrigger{Kind: aipruntime.TriggerAPI})
	if len(res.RestartFields) != 2 || res.RestartFields[0] != "server.address" {
		t.Fatalf("restart fields=%v", res.RestartFields)
	}
	res.RestartFields[0] = "mutated-dto"
	res2 := ctrl.Reload(context.Background(), aipruntime.ReloadTrigger{Kind: aipruntime.TriggerAPI})
	if res2.RestartFields[0] != "server.address" {
		t.Fatalf("mutating prior Reload DTO leaked into next result: %v", res2.RestartFields)
	}

	st := ctrl.Status()
	if len(st.History) != 1 || st.LastResult.RestartFields[0] != "server.address" {
		t.Fatalf("status=%+v", st)
	}
	st.History[0].ReasonCategory = "mutated-history"
	st.LastResult.RestartFields[0] = "status-mutated"
	st2 := ctrl.Status()
	if st2.History[0].ReasonCategory != "classify" {
		t.Fatalf("Status History must be a defensive copy; got %q", st2.History[0].ReasonCategory)
	}
	if st2.LastResult.RestartFields[0] != "server.address" {
		t.Fatalf("Status RestartFields must be a defensive copy; got %v", st2.LastResult.RestartFields)
	}
}

func TestReloadFacade_UnknownCategoryMapsInternalFailed(t *testing.T) {
	t.Parallel()
	q := &fakeReloadQuery{
		reload: func(context.Context, sdkreload.Trigger) sdkreload.Result {
			return sdkreload.Result{Category: sdkreload.ResultCategory("not-a-real-category")}
		},
	}
	ctrl := aipruntime.NewReloadControlForTest(q)
	got := ctrl.Reload(context.Background(), aipruntime.ReloadTrigger{Kind: aipruntime.TriggerAPI})
	if got.Category != aipruntime.ResultInternalFailed {
		t.Fatalf("category=%q want internal-failed", got.Category)
	}
}

func TestReloadFacade_RejectsUnknownTrigger(t *testing.T) {
	t.Parallel()
	called := false
	q := &fakeReloadQuery{
		reload: func(context.Context, sdkreload.Trigger) sdkreload.Result {
			called = true
			return sdkreload.Result{Category: sdkreload.ResultPublished}
		},
	}
	ctrl := aipruntime.NewReloadControlForTest(q)
	got := ctrl.Reload(context.Background(), aipruntime.ReloadTrigger{Kind: aipruntime.TriggerKind("watch")})
	if called {
		t.Fatal("unknown trigger must not reach coordinator")
	}
	if got.Category != aipruntime.ResultInvalid || got.ReasonCategory != "trigger" {
		t.Fatalf("got=%+v", got)
	}
}

func TestReloadFacade_NoSensitiveFields(t *testing.T) {
	t.Parallel()
	forbidden := []string{
		"Path", "YAML", "Yaml", "Secret", "Password", "Token", "DSN", "DSN",
		"Raw", "Closer", "Built", "Config", "Handler", "Fingerprint", "Digest",
		"FixedSource", "SourcePath", "FilePath", "URL", "Uri", "Credential",
	}
	for _, typ := range []reflect.Type{
		reflect.TypeFor[aipruntime.ReloadTrigger](),
		reflect.TypeFor[aipruntime.ReloadResult](),
		reflect.TypeFor[aipruntime.ReloadStatus](),
		reflect.TypeFor[aipruntime.HistoryEntry](),
	} {
		for field := range typ.Fields() {
			name := field.Name
			for _, bad := range forbidden {
				if strings.Contains(name, bad) {
					t.Fatalf("%s.%s looks sensitive/forbidden for public facade", typ.Name(), name)
				}
			}
		}
	}

	q := &fakeReloadQuery{
		status: sdkreload.Status{
			ActiveGeneration: 1,
			LastResult: sdkreload.Result{
				Category:       sdkreload.ResultPublished,
				ReasonCategory: "ok",
			},
			SourceIntegrity: "ok",
		},
	}
	ctrl := aipruntime.NewReloadControlForTest(q)
	st := ctrl.Status()
	raw := strings.ToLower(st.SourceIntegrity + st.LastResult.ReasonCategory + st.ModelGeneration)
	for _, needle := range []string{"/var/", "secret", "password", "postgres://", ".yaml"} {
		if strings.Contains(raw, needle) {
			t.Fatalf("status leaked %q via %+v", needle, st)
		}
	}
	v := reflect.ValueOf(st)
	for i := range v.NumField() {
		f := v.Field(i)
		if f.Kind() == reflect.String && strings.Contains(f.String(), "/var/secrets") {
			t.Fatalf("field %s leaked path %q", v.Type().Field(i).Name, f.String())
		}
	}
}

func TestReloadFacade_ConcurrentReloadStatusSafe(t *testing.T) {
	t.Parallel()
	var calls sync.WaitGroup
	q := &fakeReloadQuery{
		reload: func(context.Context, sdkreload.Trigger) sdkreload.Result {
			time.Sleep(time.Millisecond)
			return sdkreload.Result{Category: sdkreload.ResultNoop, ActiveGeneration: 1}
		},
		status: sdkreload.Status{
			ActiveGeneration: 1,
			LastSuccess:      sdkreload.Result{Category: sdkreload.ResultPublished, ActiveGeneration: 1},
		},
	}
	ctrl := aipruntime.NewReloadControlForTest(q)
	const n = 32
	calls.Add(n * 2)
	errCh := make(chan error, n*2)
	for range n {
		go func() {
			defer calls.Done()
			res := ctrl.Reload(context.Background(), aipruntime.ReloadTrigger{Kind: aipruntime.TriggerAPI})
			if res.Category != aipruntime.ResultNoop {
				errCh <- stringError("reload category " + string(res.Category))
			}
		}()
		go func() {
			defer calls.Done()
			st := ctrl.Status()
			if st.ActiveGeneration != 1 {
				errCh <- stringError("status generation")
			}
			_ = st.LastSuccess.Category
		}()
	}
	calls.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestRuntime_ReloadStatusUnavailableWithoutCoordinator(t *testing.T) {
	t.Parallel()
	rt := &aipruntime.Runtime{}
	res := rt.Reload(context.Background(), aipruntime.ReloadTrigger{Kind: aipruntime.TriggerAPI})
	if res.Category != aipruntime.ResultInternalFailed {
		t.Fatalf("category=%q want internal-failed", res.Category)
	}
	st := rt.ReloadStatus()
	if st.Busy {
		t.Fatal("empty runtime must not report busy")
	}
}

type stringError string

func (e stringError) Error() string { return string(e) }
