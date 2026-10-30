package backendplugin_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func slotBundle(values map[string]string) backendplugin.SecretBundle {
	bundle := backendplugin.SecretBundle{Values: make(map[string][]byte, len(values))}
	for name, secret := range values {
		bundle.Values[name] = []byte(secret)
	}
	return bundle
}

func TestDecodeOrderedSecretSlots_CanonicalOrder(t *testing.T) {
	t.Parallel()
	got, err := backendplugin.DecodeOrderedSecretSlots(slotBundle(map[string]string{
		"api_key_10": "ten",
		"api_key":    "one",
		"api_key_2":  "two",
	}), backendplugin.CredentialSlotAPIKey)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		name  string
		index int
		value string
	}{
		{"api_key", 1, "one"},
		{"api_key_2", 2, "two"},
		{"api_key_10", 10, "ten"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d slots, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Name != w.name || got[i].Index != w.index || string(got[i].Value) != w.value {
			t.Fatalf("slot %d: got %+v want %+v", i, got[i], w)
		}
	}
}

func TestDecodeOrderedSecretSlots_SparseAndTokenBase(t *testing.T) {
	t.Parallel()
	got, err := backendplugin.DecodeOrderedSecretSlots(slotBundle(map[string]string{
		"api_token_3": "three",
		"api_token":   "one",
	}), backendplugin.CredentialSlotAPIToken)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Index != 1 || got[1].Index != 3 {
		t.Fatalf("sparse slot identity must survive: %+v", got)
	}
	// Map iteration order is never meaningful: repeated decodes agree.
	for range 50 {
		again, err := backendplugin.DecodeOrderedSecretSlots(slotBundle(map[string]string{
			"api_token_3": "three",
			"api_token":   "one",
		}), backendplugin.CredentialSlotAPIToken)
		if err != nil {
			t.Fatal(err)
		}
		if len(again) != 2 || again[0].Name != got[0].Name || again[1].Name != got[1].Name {
			t.Fatalf("nondeterministic order: %+v", again)
		}
	}
}

func TestDecodeOrderedSecretSlots_RejectsMissingAndBadNames(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		values map[string]string
		base   backendplugin.CredentialSlot
	}{
		{name: "nil_bundle", values: nil, base: backendplugin.CredentialSlotAPIKey},
		{name: "empty_bundle", values: map[string]string{}, base: backendplugin.CredentialSlotAPIKey},
		{name: "foreign_single", values: map[string]string{"token": "x"}, base: backendplugin.CredentialSlotAPIKey},
		{name: "mixed_bases", values: map[string]string{"api_key": "a", "api_token": "b"}, base: backendplugin.CredentialSlotAPIKey},
		{name: "wrong_base", values: map[string]string{"api_token": "b"}, base: backendplugin.CredentialSlotAPIKey},
		{name: "slot_one_suffix", values: map[string]string{"api_key_1": "x"}, base: backendplugin.CredentialSlotAPIKey},
		{name: "slot_zero_suffix", values: map[string]string{"api_key_0": "x"}, base: backendplugin.CredentialSlotAPIKey},
		{name: "padded_suffix", values: map[string]string{"api_key_02": "x"}, base: backendplugin.CredentialSlotAPIKey},
		{name: "non_numeric_suffix", values: map[string]string{"api_key_x": "x"}, base: backendplugin.CredentialSlotAPIKey},
		{name: "trailing_suffix", values: map[string]string{"api_key_2x": "x"}, base: backendplugin.CredentialSlotAPIKey},
		{name: "uppercase", values: map[string]string{"API_KEY": "x"}, base: backendplugin.CredentialSlotAPIKey},
		{name: "empty_name", values: map[string]string{"": "x"}, base: backendplugin.CredentialSlotAPIKey},
		{name: "index_overflow", values: map[string]string{"api_key_257": "x"}, base: backendplugin.CredentialSlotAPIKey},
		{name: "index_unparseable", values: map[string]string{"api_key_99999999999999999999": "x"}, base: backendplugin.CredentialSlotAPIKey},
		{name: "empty_value", values: map[string]string{"api_key": ""}, base: backendplugin.CredentialSlotAPIKey},
		{name: "nul_value", values: map[string]string{"api_key": "ab\x00cd"}, base: backendplugin.CredentialSlotAPIKey},
		{name: "duplicate_values", values: map[string]string{"api_key": "dup-marker-secret", "api_key_2": "dup-marker-secret"}, base: backendplugin.CredentialSlotAPIKey},
		{name: "oversized_value", values: map[string]string{"api_key": strings.Repeat("k", (64<<10)+1)}, base: backendplugin.CredentialSlotAPIKey},
		{name: "total_overflow", values: map[string]string{
			"api_key":   strings.Repeat("a", 60<<10),
			"api_key_2": strings.Repeat("b", 60<<10),
			"api_key_3": strings.Repeat("c", 60<<10),
			"api_key_4": strings.Repeat("d", 60<<10),
			"api_key_5": strings.Repeat("e", 60<<10),
		}, base: backendplugin.CredentialSlotAPIKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			slots, err := backendplugin.DecodeOrderedSecretSlots(slotBundle(tc.values), tc.base)
			if err == nil {
				t.Fatalf("accepted invalid bundle: %+v", slots)
			}
			if !strings.Contains(err.Error(), "backendplugin: invalid credential slots") {
				t.Fatalf("error must carry the invalid-slots sentinel: %v", err)
			}
			for _, secret := range tc.values {
				// Short fixtures collide with English words in the message
				// ("unexpected" contains "x"); only distinctive markers
				// prove errors never echo secret material.
				if len(secret) < 8 || len(secret) >= 1024 {
					continue
				}
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error echoes secret material: %v", err)
				}
			}
		})
	}
}

func TestDecodeOrderedSecretSlots_ReturnsOwnedCopies(t *testing.T) {
	t.Parallel()
	bundle := slotBundle(map[string]string{"api_key": "live-secret"})
	first, err := backendplugin.DecodeOrderedSecretSlots(bundle, backendplugin.CredentialSlotAPIKey)
	if err != nil {
		t.Fatal(err)
	}
	for i := range first[0].Value {
		first[0].Value[i] = 'x'
	}
	second, err := backendplugin.DecodeOrderedSecretSlots(bundle, backendplugin.CredentialSlotAPIKey)
	if err != nil {
		t.Fatal(err)
	}
	if string(second[0].Value) != "live-secret" {
		t.Fatalf("decoder must return owned copies: %q", second[0].Value)
	}
}
