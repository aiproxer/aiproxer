package runtimehost

import (
	"testing"

	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
)

// TestBoundResultIndependentOfReturnedEnumeration proves boundResultName uses
// immutable canonical policy: mutating the slice returned by
// sdkreload.ResultCategories must not change binding behavior (the accessor
// returns a defensive copy; the former exported mutable var is gone).
func TestBoundResultIndependentOfReturnedEnumeration(t *testing.T) {
	t.Parallel()

	declared := []sdkreload.ResultCategory{
		sdkreload.ResultPublished,
		sdkreload.ResultNoop,
		sdkreload.ResultBusy,
		sdkreload.ResultRestartRequired,
		sdkreload.ResultRetentionBlocked,
		sdkreload.ResultInvalid,
		sdkreload.ResultSourceIntegrity,
		sdkreload.ResultCanceled,
		sdkreload.ResultPreparationFailed,
		sdkreload.ResultInternalFailed,
	}

	returned := sdkreload.ResultCategories()
	for i := range returned {
		returned[i] = "decoy"
	}

	for _, c := range declared {
		if got := boundResultName(string(c)); got != string(c) {
			t.Fatalf("after returned-slice mutation, boundResultName(%q)=%q want self", c, got)
		}
	}
	if got := boundResultName("decoy"); got != "other" {
		t.Fatalf("injected decoy must not become trusted: got %q want other", got)
	}
	if got := boundResultName(""); got != "other" {
		t.Fatalf("empty boundResultName=%q want other", got)
	}

	// Extra observer labels and stage binding remain unchanged under mutation.
	for _, extra := range []string{"ok", "accepted", "quiesce_failed", "cleanup_failed", "other"} {
		if got := boundResultName(extra); got != extra {
			t.Fatalf("extra result label boundResultName(%q)=%q", extra, got)
		}
	}
	if got := boundStageName("publish"); got != "publish" {
		t.Fatalf("boundStageName(publish)=%q under mutation", got)
	}
	if got := boundStageName("hostile"); got != "other" {
		t.Fatalf("boundStageName(hostile)=%q want other", got)
	}
}
