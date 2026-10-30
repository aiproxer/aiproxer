package jsonbody_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/jsonshape"
	"github.com/aiproxer/aiproxer/internal/jsonbody"
)

type request struct {
	Name string `json:"name"`
}

type wrappedEOFReader struct{}

func (wrappedEOFReader) Read([]byte) (int, error) { return 0, fmt.Errorf("body ended: %w", io.EOF) }

func TestDecodeRejectsOversizedContentLengthBeforeReading(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"ok"}`))
	req.ContentLength = 100
	w := httptest.NewRecorder()
	var got request
	err := jsonbody.Decode(w, req, &got, jsonbody.Policy{MaxBytes: 16})
	if !errors.Is(err, jsonbody.ErrTooLarge) {
		t.Fatalf("error=%v, want ErrTooLarge", err)
	}
	if got.Name != "" {
		t.Fatalf("destination was decoded: %+v", got)
	}
}

func TestDecodeRejectsChunkedOversize(t *testing.T) {
	t.Parallel()

	// ContentLength -1 (unknown length, e.g. chunked transfer) must reach
	// http.MaxBytesReader instead of the early ContentLength check.
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("x", 32)))
	req.ContentLength = -1
	w := httptest.NewRecorder()
	var got request
	err := jsonbody.Decode(w, req, &got, jsonbody.Policy{MaxBytes: 16})
	if !errors.Is(err, jsonbody.ErrTooLarge) {
		t.Fatalf("error=%v, want ErrTooLarge", err)
	}
}

func TestDecodeRejectsTrailingDocumentsBeforeMaterialization(t *testing.T) {
	t.Parallel()

	for _, body := range []string{`{"name":"one"}{"name":"two"}`, `{"name":"one"} trailing`} {
		t.Run(body, func(t *testing.T) {
			t.Parallel()
			var got request
			err := jsonbody.Decode(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)), &got, jsonbody.Policy{
				MaxBytes: 128,
			})
			if jsonshape.Classify(err) != jsonshape.KindMalformed {
				t.Fatalf("error=%v, classify=%q, want malformed", err, jsonshape.Classify(err))
			}
			if got.Name != "" {
				t.Fatalf("destination was materialized despite trailing data: %+v", got)
			}
		})
	}
}

func TestDecodeAllowsWrappedEOFAsEmptyBody(t *testing.T) {
	t.Parallel()

	// A body whose read surfaces a wrapped io.EOF (connection died before any
	// bytes) is treated as an empty body by AllowEmpty adapters.
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Body = io.NopCloser(wrappedEOFReader{})
	var got request
	err := jsonbody.Decode(httptest.NewRecorder(), req, &got, jsonbody.Policy{MaxBytes: 64, AllowEmpty: true})
	if err != nil {
		t.Fatalf("error=%v, want nil for wrapped-EOF body", err)
	}
}

func TestDecodeShapePreflightRunsBeforeMaterialization(t *testing.T) {
	t.Parallel()

	// 200 nested arrays exceed the request-envelope depth (128). encoding/json
	// has no depth cap, so this only fails because preflight ran first.
	body := strings.Repeat("[", 200) + "1" + strings.Repeat("]", 200)
	var got map[string]any
	err := jsonbody.Decode(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)), &got, jsonbody.Policy{MaxBytes: 1024})
	if jsonshape.Classify(err) != jsonshape.KindTooDeep {
		t.Fatalf("error=%v, classify=%q, want too_deep", err, jsonshape.Classify(err))
	}
	if len(got) != 0 {
		t.Fatalf("destination was materialized despite shape rejection: %+v", got)
	}
}

func TestDecodeAllowsConfiguredEmptyBody(t *testing.T) {
	t.Parallel()

	var got request
	err := jsonbody.Decode(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(nil)), &got, jsonbody.Policy{
		MaxBytes:   64,
		AllowEmpty: true,
	})
	if err != nil {
		t.Fatalf("error=%v, want nil", err)
	}
}

func TestDecodeAllowsConfiguredWhitespaceBody(t *testing.T) {
	t.Parallel()

	var got request
	err := jsonbody.Decode(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader(" \n\t ")), &got, jsonbody.Policy{
		MaxBytes:   64,
		AllowEmpty: true,
	})
	if err != nil {
		t.Fatalf("error=%v, want nil for whitespace-only body", err)
	}
}

func TestDecodeRejectsNonJSONWhitespaceBody(t *testing.T) {
	t.Parallel()

	// U+00A0 (NBSP) is not JSON whitespace: it must not be treated as an
	// empty body by AllowEmpty and must not be classified as MalformedEmpty.
	for _, allowEmpty := range []bool{false, true} {
		var got request
		err := jsonbody.Decode(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader("\u00a0")), &got, jsonbody.Policy{
			MaxBytes:   64,
			AllowEmpty: allowEmpty,
		})
		if jsonshape.Classify(err) != jsonshape.KindMalformed {
			t.Fatalf("AllowEmpty=%v: error=%v, want malformed", allowEmpty, err)
		}
		if errors.Is(err, jsonbody.ErrEmpty) {
			t.Fatalf("AllowEmpty=%v: NBSP body classified as empty", allowEmpty)
		}
	}
}

func TestDecodePreservesContextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"ok"}`)).WithContext(ctx)
	var got request
	err := jsonbody.Decode(httptest.NewRecorder(), req, &got, jsonbody.Policy{MaxBytes: 128})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v, want context.Canceled", err)
	}
}

func TestDecodeUsesStandardJSONNumberBehaviorAfterPreflight(t *testing.T) {
	t.Parallel()

	var got map[string]any
	err := jsonbody.Decode(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"value":1}`)), &got, jsonbody.Policy{MaxBytes: 64})
	if err != nil {
		t.Fatalf("error=%v", err)
	}
	value, ok := got["value"].(float64)
	if !ok || value != 1 {
		t.Fatalf("decoded value=%#v, want float64(1)", got["value"])
	}
}
