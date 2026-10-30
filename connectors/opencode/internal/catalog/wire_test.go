package catalog

import "testing"

func TestNativeID_usesProviderRawModelID(t *testing.T) {
	t.Parallel()

	cases := []struct {
		kind BackendKind
		raw  string
		want string
	}{
		{BackendGo, "kimi-k2.7-code", "kimi-k2.7-code"},
		{BackendZen, "kimi-k2.7-code", "kimi-k2.7-code"},
		{BackendGo, "gpt-5.4", "gpt-5.4"},
		{BackendZen, "gpt-5.4", "gpt-5.4"},
	}

	for _, tc := range cases {
		t.Run(string(tc.kind)+"/"+tc.raw, func(t *testing.T) {
			t.Parallel()
			if got := NativeID(tc.kind, tc.raw); got != tc.want {
				t.Fatalf("NativeID(%q, %q) = %q, want %q", tc.kind, tc.raw, got, tc.want)
			}
		})
	}
}

func TestInventoryModels_snapshotShape(t *testing.T) {
	t.Parallel()

	entries := []ModelEntry{
		{RawID: "kimi-k2.7-code", DisplayName: "Kimi K2.7 Code"},
		{RawID: "gpt-5.4", DisplayName: "GPT 5.4"},
	}
	models := InventoryModels(BackendZen, entries, testKeywordFallbackResolver())
	if len(models) != 2 {
		t.Fatalf("models = %+v", models)
	}
	want := map[string]struct {
		canonical string
		native    string
		display   string
	}{
		"kimi-k2.7-code": {canonical: "moonshotai/kimi-k2.7-code", native: "kimi-k2.7-code", display: "Kimi K2.7 Code"},
		"gpt-5.4":        {canonical: "openai/gpt-5.4", native: "gpt-5.4", display: "GPT 5.4"},
	}
	for _, m := range models {
		raw := m.NativeID
		w, ok := want[raw]
		if !ok {
			t.Fatalf("unexpected model %+v", m)
		}
		if m.CanonicalID != w.canonical || m.NativeID != w.native || m.DisplayName != w.display {
			t.Fatalf("model = %+v, want canonical=%q native=%q display=%q", m, w.canonical, w.native, w.display)
		}
		delete(want, raw)
	}
	if len(want) != 0 {
		t.Fatalf("missing models: %+v", want)
	}
}
