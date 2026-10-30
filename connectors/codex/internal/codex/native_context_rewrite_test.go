package codex

import (
	"testing"
	"time"
)

func rewriteTestHistory(items ...inputItem) NativeHistory {
	history := NativeHistory{Items: cloneInputItems(items)}
	history.Fingerprints = make([]string, 0, len(items))
	for _, item := range history.Items {
		fp, err := fingerprintNativeItem(item)
		if err != nil {
			panic(err)
		}
		history.Fingerprints = append(history.Fingerprints, fp)
	}
	history.Boundaries = nativeTrajectoryBoundaries(history.Items)
	return history
}

func TestRewriteNativeHistory_ExactPrefixPreservesLiveSuffix(t *testing.T) {
	t.Parallel()
	old := textMessageItem{Type: "message", Role: "user", Content: "old"}
	live := richMessageItem{Type: "message", Role: "user", Content: []contentBlock{inputTextPart{Text: "live"}, inputImagePart{ImageURL: "data:image/png;base64,opaque"}}}
	later := textMessageItem{Type: "message", Role: "assistant", Content: "later"}
	history := rewriteTestHistory(old, live, later)
	checkpoint := testCheckpoint(testCheckpointKey("rewrite"), "replacement")
	checkpoint.SourcePrefixFP = append([]string(nil), history.Fingerprints[:1]...)
	checkpoint.Replacement = []inputItem{opaqueResponseItem{raw: []byte(`{"type":"compaction","id":"cmp-1","encrypted_content":"opaque"}`)}}

	rewritten, applied, err := rewriteNativeHistory(history, checkpoint)
	if err != nil || !applied {
		t.Fatalf("rewrite = applied:%v err:%v", applied, err)
	}
	if len(rewritten.Items) != 3 {
		t.Fatalf("rewritten items = %d, want 3", len(rewritten.Items))
	}
	got, ok := rewritten.Items[1].(richMessageItem)
	if !ok {
		t.Fatalf("expected richMessageItem at index 1: %#v", rewritten.Items[1])
	}
	if len(got.Content) != 2 {
		t.Fatalf("live suffix changed: %#v", got)
	}
	firstPart, ok1 := got.Content[0].(inputTextPart)
	secondPart, ok2 := got.Content[1].(inputImagePart)
	if !ok1 || firstPart.Text != "live" || !ok2 || secondPart.ImageURL != "data:image/png;base64,opaque" {
		t.Fatalf("live suffix changed: %#v", got)
	}
	gotLater, ok := rewritten.Items[2].(textMessageItem)
	if !ok || gotLater.Content != "later" {
		t.Fatalf("later suffix changed: %#v", rewritten.Items[2])
	}
	if len(rewritten.Fingerprints) != len(rewritten.Items) || len(rewritten.Boundaries) != len(rewritten.Items)+1 {
		t.Fatalf("effective metadata not recomputed: %+v", rewritten)
	}

	rewritten.Fingerprints[0] = "changed"
	rewrittenRich, ok := rewritten.Items[1].(richMessageItem)
	if !ok {
		t.Fatalf("expected richMessageItem at index 1: %#v", rewritten.Items[1])
	}
	rewrittenRich.Content[0] = inputTextPart{Text: "changed"}
	historyRich, ok := history.Items[1].(richMessageItem)
	if !ok {
		t.Fatalf("expected richMessageItem in history: %#v", history.Items[1])
	}
	historyPart, ok := historyRich.Content[0].(inputTextPart)
	if !ok {
		t.Fatalf("expected inputTextPart in history: %#v", historyRich.Content[0])
	}
	if history.Fingerprints[0] == "changed" || historyPart.Text != "live" {
		t.Fatal("rewrite exposed source history state")
	}
}

func TestRewriteNativeHistory_MismatchIsSafeMissForForksAndStaticDrift(t *testing.T) {
	t.Parallel()
	history := rewriteTestHistory(
		textMessageItem{Type: "message", Role: "user", Content: "old"},
		textMessageItem{Type: "message", Role: "user", Content: "live"},
	)
	checkpoint := testCheckpoint(testCheckpointKey("rewrite-miss"), "replacement")
	checkpoint.SourcePrefixFP = []string{"not-the-history"}
	for _, name := range []string{"edit", "rollback", "fork", "truncation", "reorder", "static-shape"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			candidate := history
			switch name {
			case "edit", "fork":
				candidate = rewriteTestHistory(textMessageItem{Type: "message", Role: "user", Content: name}, history.Items[1])
			case "rollback", "truncation":
				candidate = rewriteTestHistory(history.Items[0])
			case "reorder":
				candidate = rewriteTestHistory(history.Items[1], history.Items[0])
			case "static-shape":
				candidate = rewriteTestHistory(history.Items[0], textMessageItem{Type: "message", Role: "user", Content: "different-tools"})
			}
			_, applied, err := rewriteNativeHistory(candidate, checkpoint)
			if err != nil {
				t.Fatalf("miss returned error: %v", err)
			}
			if applied {
				t.Fatal("mismatch applied checkpoint")
			}
		})
	}
}

func TestRewriteNativeHistory_RejectsStaleDerivedMetadata(t *testing.T) {
	t.Parallel()
	history := rewriteTestHistory(
		textMessageItem{Type: "message", Role: "user", Content: "original"},
		textMessageItem{Type: "message", Role: "user", Content: "live"},
	)
	checkpoint := testCheckpoint(testCheckpointKey("stale-metadata"), "replacement")
	checkpoint.SourcePrefixFP = append([]string(nil), history.Fingerprints[:1]...)
	history.Items[0] = textMessageItem{Type: "message", Role: "user", Content: "edited-with-stale-fingerprint"}

	_, applied, err := rewriteNativeHistory(history, checkpoint)
	if err != nil {
		t.Fatalf("stale metadata returned error: %v", err)
	}
	if applied {
		t.Fatal("checkpoint applied against stale derived metadata")
	}
}

func TestRewriteNativeHistoryWithKey_RejectsAuthorityAndStaticIdentityDrift(t *testing.T) {
	t.Parallel()
	history := rewriteTestHistory(
		textMessageItem{Type: "message", Role: "user", Content: "old"},
		textMessageItem{Type: "message", Role: "user", Content: "live"},
	)
	key := testCheckpointKey("authority")
	checkpoint := testCheckpoint(key, "replacement")
	checkpoint.SourcePrefixFP = []string{history.Fingerprints[0]}
	for _, mutate := range []func(*CheckpointKey){
		func(k *CheckpointKey) { k.SessionID += "-fork" },
		func(k *CheckpointKey) { k.AccountID += "-rotated" },
		func(k *CheckpointKey) { k.Model += "-downshift" },
		func(k *CheckpointKey) { k.CompHash += "-changed" },
		func(k *CheckpointKey) { k.InstructionsFP += "-changed" },
		func(k *CheckpointKey) { k.ToolsFP += "-changed" },
		func(k *CheckpointKey) { k.PromptCacheKey += "-changed" },
		func(k *CheckpointKey) { k.ClientFamily += "-changed" },
		func(k *CheckpointKey) { k.ContinuityMode = "best_effort" },
	} {
		candidate := key
		mutate(&candidate)
		_, applied, err := rewriteNativeHistoryWithKey(history, candidate, checkpoint)
		if err != nil || applied {
			t.Fatalf("identity drift applied checkpoint: applied=%v err=%v", applied, err)
		}
	}
}

func TestRewriteNativeHistory_RejectsInvalidStoredCheckpointAndSupportsCheckpointOverCheckpoint(t *testing.T) {
	t.Parallel()
	history := rewriteTestHistory(
		textMessageItem{Type: "message", Role: "user", Content: "original"},
		textMessageItem{Type: "message", Role: "user", Content: "live"},
	)
	invalid := testCheckpoint(testCheckpointKey("invalid"), "invalid")
	invalid.SourcePrefixFP = nil
	if _, applied, err := rewriteNativeHistory(history, invalid); err == nil || applied {
		t.Fatalf("invalid checkpoint result applied=%v err=%v", applied, err)
	}

	first := testCheckpoint(testCheckpointKey("chain"), "first")
	first.SourcePrefixFP = []string{history.Fingerprints[0]}
	first.Replacement = []inputItem{textMessageItem{Type: "message", Role: "assistant", Content: "summary-one"}}
	compacted, applied, err := rewriteNativeHistory(history, first)
	if err != nil || !applied {
		t.Fatalf("first rewrite applied=%v err=%v", applied, err)
	}
	second := testCheckpoint(testCheckpointKey("chain"), "second")
	second.SourcePrefixFP = append([]string(nil), compacted.Fingerprints[:1]...)
	second.Replacement = []inputItem{textMessageItem{Type: "message", Role: "assistant", Content: "summary-two"}}
	final, applied, err := rewriteNativeHistory(compacted, second)
	if err != nil || !applied || len(final.Items) != 2 {
		t.Fatalf("second rewrite = %#v applied=%v err=%v", final, applied, err)
	}
	firstItem, ok1 := final.Items[0].(textMessageItem)
	secondItem, ok2 := final.Items[1].(textMessageItem)
	if !ok1 || !ok2 || firstItem.Content != "summary-two" || secondItem.Content != "live" {
		t.Fatalf("checkpoint-over-checkpoint lost suffix: %#v", final.Items)
	}
}

func TestRewriteNativeHistory_ExpiredCheckpointIsSafeMiss(t *testing.T) {
	t.Parallel()
	history := rewriteTestHistory(textMessageItem{Type: "message", Role: "user", Content: "live"})
	checkpoint := testCheckpoint(testCheckpointKey("expired"), "replacement")
	checkpoint.SourcePrefixFP = []string{history.Fingerprints[0]}
	checkpoint.CreatedAt = time.Unix(10, 0)
	checkpoint.ExpiresAt = time.Unix(11, 0)
	_, applied, err := rewriteNativeHistory(history, checkpoint)
	if err != nil || applied {
		t.Fatalf("expired checkpoint was not a safe miss: applied=%v err=%v", applied, err)
	}
}
