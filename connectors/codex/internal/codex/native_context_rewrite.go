package codex

import (
	"reflect"
	"time"
)

// rewriteNativeHistory applies a checkpoint only when its complete source
// prefix is present at the start of the current exact history. A mismatch is a
// normal optimization miss, so callers retain the original history.
func rewriteNativeHistory(history NativeHistory, checkpoint NativeCheckpoint) (NativeHistory, bool, error) {
	return rewriteNativeHistoryAt(history, checkpoint, time.Now())
}

func rewriteNativeHistoryAt(history NativeHistory, checkpoint NativeCheckpoint, now time.Time) (NativeHistory, bool, error) {
	if !validStoredCheckpoint(checkpoint) {
		return NativeHistory{}, false, ErrCheckpointInvalid
	}
	if !nativeHistoryMetadataMatches(history) {
		return cloneNativeHistory(history), false, nil
	}
	if !checkpoint.ExpiresAt.IsZero() && !checkpoint.ExpiresAt.After(now) {
		return cloneNativeHistory(history), false, nil
	}
	prefixLen := len(checkpoint.SourcePrefixFP)
	if prefixLen > len(history.Items) || prefixLen > len(history.Fingerprints) {
		return cloneNativeHistory(history), false, nil
	}
	for i, want := range checkpoint.SourcePrefixFP {
		if history.Fingerprints[i] != want {
			return cloneNativeHistory(history), false, nil
		}
	}
	items := append(cloneInputItems(checkpoint.Replacement), cloneInputItems(history.Items[prefixLen:])...)
	rewritten, err := buildNativeHistoryFromItems(items)
	if err != nil {
		return NativeHistory{}, false, ErrCheckpointInvalid
	}
	return rewritten, true, nil
}

func nativeHistoryMetadataMatches(history NativeHistory) bool {
	if len(history.Fingerprints) != len(history.Items) || len(history.Boundaries) != len(history.Items)+1 {
		return false
	}
	if history.OpaqueMetadataTokens != nil && len(history.OpaqueMetadataTokens) != len(history.Items) {
		return false
	}
	expected, err := buildNativeHistoryFromItems(history.Items)
	if err != nil {
		return false
	}
	return reflect.DeepEqual(expected.Fingerprints, history.Fingerprints) && reflect.DeepEqual(expected.Boundaries, history.Boundaries)
}

// rewriteNativeHistoryWithKey includes the static identity check used by the
// coordinator before prefix matching. It prevents a valid prefix from another
// account, model, or request shape being treated as reusable state.
func rewriteNativeHistoryWithKey(history NativeHistory, key CheckpointKey, checkpoint NativeCheckpoint) (NativeHistory, bool, error) {
	if !validCheckpointKey(key) || checkpoint.Key != key {
		return cloneNativeHistory(history), false, nil
	}
	return rewriteNativeHistory(history, checkpoint)
}

func buildNativeHistoryFromItems(items []inputItem) (NativeHistory, error) {
	result := NativeHistory{Items: cloneInputItems(items)}
	result.Fingerprints = make([]string, 0, len(result.Items))
	for _, item := range result.Items {
		fp, err := fingerprintNativeItem(item)
		if err != nil {
			return NativeHistory{}, err
		}
		result.Fingerprints = append(result.Fingerprints, fp)
	}
	result.Boundaries = nativeTrajectoryBoundaries(result.Items)
	return result, nil
}

func cloneNativeHistory(history NativeHistory) NativeHistory {
	return NativeHistory{
		Items:                cloneInputItems(history.Items),
		Fingerprints:         append([]string(nil), history.Fingerprints...),
		Boundaries:           append([]TrajectoryBoundary(nil), history.Boundaries...),
		OpaqueMetadataTokens: append([]int64(nil), history.OpaqueMetadataTokens...),
	}
}
