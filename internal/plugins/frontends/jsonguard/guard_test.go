package jsonguard_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/jsonguard"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestPreflightLimits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		data     string
		limits   jsonguard.Limits
		wantKind jsonguard.Kind
	}{
		{name: "bytes at limit", data: `{"x":1}`, limits: jsonguard.Limits{MaxBytes: int64(len(`{"x":1}`))}},
		{name: "bytes over limit", data: `{"x":1}`, limits: jsonguard.Limits{MaxBytes: int64(len(`{"x":1}`) - 1)}, wantKind: jsonguard.KindTooLarge},
		{name: "depth arrays at limit", data: `[[[1]]]`, limits: jsonguard.Limits{MaxDepth: 3}},
		{name: "depth arrays over limit", data: `[[[1]]]`, limits: jsonguard.Limits{MaxDepth: 2}, wantKind: jsonguard.KindTooDeep},
		{name: "depth objects at limit", data: `{"a":{"b":{"c":1}}}`, limits: jsonguard.Limits{MaxDepth: 3}},
		{name: "depth objects over limit", data: `{"a":{"b":{"c":1}}}`, limits: jsonguard.Limits{MaxDepth: 2}, wantKind: jsonguard.KindTooDeep},
		{name: "tokens at limit", data: `[1,2,3]`, limits: jsonguard.Limits{MaxTokens: 5}},
		{name: "tokens over limit", data: `[1,2,3]`, limits: jsonguard.Limits{MaxTokens: 4}, wantKind: jsonguard.KindTooManyTokens},
		{name: "array elements at limit", data: `[1,2,3]`, limits: jsonguard.Limits{MaxArrayElems: 3}},
		{name: "array elements over limit", data: `[1,2,3]`, limits: jsonguard.Limits{MaxArrayElems: 2}, wantKind: jsonguard.KindTooManyItems},
		{name: "object keys at limit", data: `{"a":1,"b":2}`, limits: jsonguard.Limits{MaxObjectKeys: 2}},
		{name: "object keys over limit", data: `{"a":1,"b":2}`, limits: jsonguard.Limits{MaxObjectKeys: 1}, wantKind: jsonguard.KindTooManyItems},
		{name: "string bytes at limit", data: `"abcd"`, limits: jsonguard.Limits{MaxStringBytes: 4}},
		{name: "string bytes over limit", data: `"abcd"`, limits: jsonguard.Limits{MaxStringBytes: 3}, wantKind: jsonguard.KindStringTooLong},
		{name: "key bytes at limit", data: `{"abcd":1}`, limits: jsonguard.Limits{MaxKeyBytes: 4}},
		{name: "key bytes over limit", data: `{"abcd":1}`, limits: jsonguard.Limits{MaxKeyBytes: 3}, wantKind: jsonguard.KindKeyTooLong},
		{name: "escaped string decoded bytes at limit", data: `"\u20ac"`, limits: jsonguard.Limits{MaxStringBytes: 3}},
		{name: "escaped string decoded bytes over limit", data: `"\u20ac"`, limits: jsonguard.Limits{MaxStringBytes: 2}, wantKind: jsonguard.KindStringTooLong},
		{name: "huge number token over limit", data: `[123456789012345678901234567890]`, limits: jsonguard.Limits{MaxTokens: 1}, wantKind: jsonguard.KindTooManyTokens},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := jsonguard.Preflight([]byte(tt.data), tt.limits)
			if tt.wantKind == "" {
				if err != nil {
					t.Fatalf("Preflight() error = %v", err)
				}
				if result.Bytes != len(tt.data) || result.Tokens <= 0 || result.MaxDepth < 0 {
					t.Fatalf("unexpected result: %+v", result)
				}
				return
			}
			if got := jsonguard.Classify(err); got != tt.wantKind {
				t.Fatalf("Classify(error) = %q, want %q (err=%v)", got, tt.wantKind, err)
			}
		})
	}
}

func TestPreflightMalformed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data string
	}{
		{name: "empty", data: ""},
		{name: "whitespace", data: " \n\t "},
		{name: "early malformed", data: `{`},
		{name: "late malformed", data: `{"a":[1,2,]}`},
		{name: "trailing token", data: `{} []`},
		{name: "trailing garbage", data: `{} nope`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := jsonguard.Preflight([]byte(tt.data), jsonguard.Limits{})
			if got := jsonguard.Classify(err); got != jsonguard.KindMalformed {
				t.Fatalf("Classify(error) = %q, want %q (err=%v)", got, jsonguard.KindMalformed, err)
			}
		})
	}
}

func TestPreflightObjectValueTracking(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		data      string
		limits    jsonguard.Limits
		wantKind  jsonguard.Kind
		wantValue int
	}{
		{
			name:   "string value counts one key",
			data:   `{"a":"b"}`,
			limits: jsonguard.Limits{MaxObjectKeys: 1},
		},
		{
			name:      "second key rejects after string value",
			data:      `{"a":"b","c":"d"}`,
			limits:    jsonguard.Limits{MaxObjectKeys: 1},
			wantKind:  jsonguard.KindTooManyItems,
			wantValue: 2,
		},
		{
			name:   "nested object value preserves parent key count",
			data:   `{"a":{"b":"c"}}`,
			limits: jsonguard.Limits{MaxObjectKeys: 1},
		},
		{
			name:   "array value strings count as array elements",
			data:   `{"a":["b","c"]}`,
			limits: jsonguard.Limits{MaxObjectKeys: 1, MaxArrayElems: 2},
		},
		{
			name:   "root array string elements are not object keys",
			data:   `["a","b"]`,
			limits: jsonguard.Limits{MaxObjectKeys: 1, MaxArrayElems: 2},
		},
		{
			name:      "array string elements reject on array limit",
			data:      `{"a":["b","c"]}`,
			limits:    jsonguard.Limits{MaxObjectKeys: 1, MaxArrayElems: 1},
			wantKind:  jsonguard.KindTooManyItems,
			wantValue: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := jsonguard.Preflight([]byte(tt.data), tt.limits)
			if tt.wantKind == "" {
				if err != nil {
					t.Fatalf("Preflight() error = %v", err)
				}
				return
			}
			if got := jsonguard.Classify(err); got != tt.wantKind {
				t.Fatalf("Classify(error) = %q, want %q (err=%v)", got, tt.wantKind, err)
			}
			var guardErr *jsonguard.Error
			if !errors.As(err, &guardErr) {
				t.Fatalf("error %T does not unwrap to *jsonguard.Error", err)
			}
			if guardErr.Value != tt.wantValue {
				t.Fatalf("Error.Value = %d, want %d", guardErr.Value, tt.wantValue)
			}
		})
	}
}

func TestErrorClassification(t *testing.T) {
	t.Parallel()

	if got := jsonguard.Classify(nil); got != "" {
		t.Fatalf("Classify(nil) = %q, want empty", got)
	}
	if got := jsonguard.Classify(errors.New("x")); got != "" {
		t.Fatalf("Classify(other) = %q, want empty", got)
	}
	if !jsonguard.TooLarge(&jsonguard.Error{Kind: jsonguard.KindTooLarge}) {
		t.Fatal("TooLarge() = false, want true")
	}
}

func TestPreflightDuplicateNumberAndUTF8(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		data     string
		limits   jsonguard.Limits
		wantKind jsonguard.Kind
	}{
		{name: "default envelope accepts duplicate member", data: `{"a":1,"a":2}`},
		{name: "explicit reject duplicates", data: `{"a":1,"a":2}`, limits: jsonguard.Limits{RejectDuplicateNames: true}, wantKind: jsonguard.KindDuplicateName},
		{name: "accept false counts duplicate keys for MaxObjectKeys", data: `{"a":1,"a":2}`, limits: jsonguard.Limits{MaxObjectKeys: 1}, wantKind: jsonguard.KindTooManyItems},
		{name: "distinct second key hits MaxObjectKeys", data: `{"a":1,"b":2}`, limits: jsonguard.Limits{MaxObjectKeys: 1}, wantKind: jsonguard.KindTooManyItems},
		{name: "sibling names across objects ok", data: `[{"a":1},{"a":2}]`},
		{name: "number over", data: `123456`, limits: jsonguard.Limits{MaxNumberBytes: 5}, wantKind: jsonguard.KindNumberTooLong},
		{name: "invalid utf8", data: "\"\xff\"", wantKind: jsonguard.KindInvalidUTF8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := jsonguard.Preflight([]byte(tt.data), tt.limits)
			if tt.wantKind == "" {
				if err != nil {
					t.Fatalf("Preflight() error = %v", err)
				}
				return
			}
			if got := jsonguard.Classify(err); got != tt.wantKind {
				t.Fatalf("Classify(error) = %q, want %q (err=%v)", got, tt.wantKind, err)
			}
			if strings.Contains(err.Error(), "\xff") {
				t.Fatalf("error leaked invalid bytes: %v", err)
			}
		})
	}
}

func TestNormalizeLimits(t *testing.T) {
	t.Parallel()

	defaults := jsonguard.DefaultLimits()
	got := jsonguard.NormalizeLimits(jsonguard.Limits{MaxDepth: 2, MaxBytes: -1})
	if got.MaxDepth != 2 {
		t.Fatalf("MaxDepth = %d, want 2", got.MaxDepth)
	}
	if got.MaxBytes != defaults.MaxBytes {
		t.Fatalf("MaxBytes = %d, want default %d", got.MaxBytes, defaults.MaxBytes)
	}
	if defaults.MaxTokens <= 0 || defaults.MaxArrayElems <= 0 || defaults.MaxObjectKeys <= 0 || defaults.MaxStringBytes <= 0 || defaults.MaxKeyBytes <= 0 {
		t.Fatalf("defaults must be positive: %+v", defaults)
	}
}

func TestPreflightDefaultStringLimitAllowsCanonicalText(t *testing.T) {
	t.Parallel()

	largeText := strings.Repeat("a", 2<<20)
	if len(largeText) >= aipapi.MaxPartTextBytes {
		t.Fatalf("test payload is too large: %d >= %d", len(largeText), aipapi.MaxPartTextBytes)
	}

	tests := []struct {
		name string
		data string
	}{
		{name: "root string", data: `"` + largeText + `"`},
		{name: "object string", data: `{"input":"` + largeText + `"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := jsonguard.Preflight([]byte(tt.data), jsonguard.Limits{}); err != nil {
				t.Fatalf("Preflight() error = %v", err)
			}
		})
	}
}

func TestReadAndPreflight(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		body     string
		limits   jsonguard.Limits
		wantKind jsonguard.Kind
		wantCode int
	}{
		{name: "ok", body: `{"x":1}`, limits: jsonguard.Limits{MaxBytes: 20}, wantCode: http.StatusOK},
		{name: "reader max bytes", body: `{"x":"abcdef"}`, limits: jsonguard.Limits{MaxBytes: 8}, wantKind: jsonguard.KindTooLarge, wantCode: http.StatusRequestEntityTooLarge},
		{name: "preflight max bytes", body: `{"x":1}`, limits: jsonguard.Limits{MaxBytes: int64(len(`{"x":1}`) - 1)}, wantKind: jsonguard.KindTooLarge, wantCode: http.StatusRequestEntityTooLarge},
		{name: "malformed", body: `{`, limits: jsonguard.Limits{MaxBytes: 20}, wantKind: jsonguard.KindMalformed, wantCode: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.body))
			w := httptest.NewRecorder()
			var data []byte
			var result jsonguard.Result
			var err error
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				data, result, err = jsonguard.ReadAndPreflight(w, r, tt.limits)
				if err != nil {
					if jsonguard.TooLarge(err) {
						http.Error(w, http.StatusText(http.StatusRequestEntityTooLarge), http.StatusRequestEntityTooLarge)
						return
					}
					http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
					return
				}
				w.WriteHeader(http.StatusOK)
			})
			handler.ServeHTTP(w, r)
			if tt.wantKind == "" {
				if err != nil {
					t.Fatalf("ReadAndPreflight() error = %v", err)
				}
				if w.Code != tt.wantCode {
					t.Fatalf("status = %d, want %d", w.Code, tt.wantCode)
				}
				if string(data) != tt.body || result.Bytes != len(tt.body) {
					t.Fatalf("unexpected data/result: %q %+v", data, result)
				}
				return
			}
			if got := jsonguard.Classify(err); got != tt.wantKind {
				t.Fatalf("Classify(error) = %q, want %q (err=%v)", got, tt.wantKind, err)
			}
			if w.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", w.Code, tt.wantCode)
			}
		})
	}
}

func FuzzPreflight(f *testing.F) {
	seeds := [][]byte{
		[]byte(`{"x":1}`),
		[]byte(`{`),
		[]byte(`[[[[[[[[[[0]]]]]]]]]]`),
		[]byte(`[` + strings.Repeat(`1,`, 128) + `1]`),
		[]byte(`"` + strings.Repeat(`a`, 4096) + `"`),
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		result, err := jsonguard.Preflight(data, jsonguard.DefaultLimits())
		if err != nil {
			return
		}
		if !json.Valid(data) {
			t.Fatalf("Preflight succeeded but encoding/json.Valid returned false for %q", data)
		}
		if result.Bytes != len(data) || result.Tokens <= 0 || result.MaxDepth < 0 {
			t.Fatalf("unexpected result: %+v for %q", result, data)
		}
	})
}

func BenchmarkPreflight(b *testing.B) {
	benches := []struct {
		name   string
		data   []byte
		limits jsonguard.Limits
	}{
		{name: "valid small", data: []byte(`{"model":"x","input":"hello"}`)},
		{name: "valid near-ish limit", data: []byte(`[` + strings.TrimRight(strings.Repeat(`{"x":"`+strings.Repeat("a", 32)+`"},`, 1024), ",") + `]`)},
		{name: "deep reject", data: []byte(strings.Repeat(`[`, 64) + `0` + strings.Repeat(`]`, 64)), limits: jsonguard.Limits{MaxDepth: 16}},
		{name: "fanout reject", data: []byte(`[` + strings.TrimRight(strings.Repeat(`1,`, 2048), ",") + `]`), limits: jsonguard.Limits{MaxArrayElems: 128}},
		{name: "huge string reject", data: []byte(`"` + strings.Repeat(`a`, 8192) + `"`), limits: jsonguard.Limits{MaxStringBytes: 128}},
	}

	for _, bench := range benches {
		b.Run(bench.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_, _ = jsonguard.Preflight(bench.data, bench.limits)
			}
		})
	}
}
