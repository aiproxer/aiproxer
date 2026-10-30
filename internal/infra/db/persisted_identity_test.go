package db

import (
	"strings"
	"testing"
)

func TestPersistedIdentityKeyV2FramesEachField(t *testing.T) {
	preimage := "v2\x00store\x00event\x00revision"
	key := PersistedIdentityKeyV2(preimage)
	want := "obs:v2:" + "2:v2" + "5:store" + "5:event" + "8:revision"
	if key != want {
		t.Fatalf("PersistedIdentityKeyV2(%q) = %q, want %q", preimage, key, want)
	}
	if strings.ContainsRune(key, 0) {
		t.Fatalf("persisted key %q must be NUL-free for Bun's SQL formatter", key)
	}
}

func TestPersistedIdentityKeyV2SeparatesLegacyStripCollisions(t *testing.T) {
	first := "v2\x00store\x00evt-1\x002"
	second := "v2\x00store\x00evt-\x0012"
	if LegacyPersistedText(first) != LegacyPersistedText(second) {
		t.Fatal("fixture must collide under the legacy stripped encoding")
	}
	if PersistedIdentityKeyV2(first) == PersistedIdentityKeyV2(second) {
		t.Fatal("distinct preimages must not share a v2 persisted key")
	}
	for _, key := range []string{PersistedIdentityKeyV2(first), PersistedIdentityKeyV2(second)} {
		if !strings.HasPrefix(key, "obs:v2:") {
			t.Fatalf("key %q must carry the obs:v2: prefix", key)
		}
		if strings.ContainsRune(key, 0) {
			t.Fatalf("key %q must be NUL-free", key)
		}
	}
}

func TestPersistedIdentityKeyV2StaysInjectiveAcrossEmptyAndAdjacentFields(t *testing.T) {
	preimages := []string{
		"a\x00b",
		"a\x00b\x00",
		"a\x00\x00b",
		"ab",
		"a",
		"",
		"v2\x00\x00\x00\x00",
	}
	seen := make(map[string]string, len(preimages))
	for _, preimage := range preimages {
		key := PersistedIdentityKeyV2(preimage)
		if previous, ok := seen[key]; ok {
			t.Fatalf("preimages %q and %q both persist %q", previous, preimage, key)
		}
		seen[key] = preimage
	}
}

func TestPersistedKeyV2SeparatesConcatenatingParts(t *testing.T) {
	first := PersistedKeyV2("customer_submission_fee_claim", "ac", "ct1")
	second := PersistedKeyV2("customer_submission_fee_claim", "acc", "t1")
	if first == second {
		t.Fatal("distinct part tuples must not share a persisted key")
	}
	for _, key := range []string{first, second} {
		if !strings.HasPrefix(key, "customer_submission_fee_claim:v2:") {
			t.Fatalf("key %q must carry the v2 prefix", key)
		}
		if strings.ContainsRune(key, 0) {
			t.Fatalf("key %q must be NUL-free", key)
		}
	}
}
