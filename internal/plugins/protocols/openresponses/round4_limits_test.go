package openresponses

import (
	"errors"
	"strings"
	"testing"
)

func TestRound4DecodeContentPartsRejectsInvalidImageObjectURL(t *testing.T) {
	t.Parallel()
	_, err := decodeContentParts([]byte(`[{"type":"input_image","image_url":{"url":123}}]`))
	if err == nil {
		t.Fatal("expected invalid image_url object to be rejected")
	}
	if !errors.Is(err, ErrDecodeFailed) {
		t.Fatalf("error = %v, want ErrDecodeFailed", err)
	}
	if strings.Contains(err.Error(), `{"url":123}`) {
		t.Fatalf("error retained raw JSON fallback: %v", err)
	}
}

func TestRound4DecodeRequestEnforcesOperationalLimits(t *testing.T) {
	t.Parallel()
	base := DefaultLimits()

	t.Run("item count", func(t *testing.T) {
		t.Parallel()
		limits := base
		limits.MaxItemCount = 1
		body := []byte(`{"model":"m","input":[{"type":"message","role":"user"},{"type":"message","role":"user"}]}`)
		_, _, err := DecodeRequest(body, limits)
		assertLimitParam(t, err, "item_count")
	})

	t.Run("schema size", func(t *testing.T) {
		t.Parallel()
		limits := base
		limits.MaxSchemaSizeBytes = 4
		body := []byte(`{"model":"m","input":"hi","tools":[{"type":"function","name":"f","parameters":{"type":"object"}}]}`)
		_, _, err := DecodeRequest(body, limits)
		assertLimitParam(t, err, "schema_size")
	})

	t.Run("extension opaque payload", func(t *testing.T) {
		t.Parallel()
		limits := base
		limits.MaxOpaquePayloadSizeBytes = 2
		body := []byte(`{"model":"m","input":[{"type":"vendor:item","data":"large"}]}`)
		_, _, err := DecodeRequest(body, limits)
		assertLimitParam(t, err, "opaque_payload_size")
	})

	t.Run("compaction opaque payload", func(t *testing.T) {
		t.Parallel()
		limits := base
		limits.MaxOpaquePayloadSizeBytes = 2
		body := []byte(`{"model":"m","input":[{"type":"compaction","opaque":"large"}]}`)
		_, _, err := DecodeRequest(body, limits)
		assertLimitParam(t, err, "opaque_payload_size")
	})

	t.Run("item reference", func(t *testing.T) {
		t.Parallel()
		limits := base
		limits.MaxContinuationRefSizeBytes = 3
		body := []byte(`{"model":"m","input":[{"type":"item_reference","id":"long"}]}`)
		_, _, err := DecodeRequest(body, limits)
		assertLimitParam(t, err, "continuation_ref_size")
	})

	t.Run("previous response reference", func(t *testing.T) {
		t.Parallel()
		limits := base
		limits.MaxContinuationRefSizeBytes = 3
		body := []byte(`{"model":"m","previous_response_id":"long"}`)
		_, _, err := DecodeRequest(body, limits)
		assertLimitParam(t, err, "continuation_ref_size")
	})
}

func TestRound4DecodeRequestUsesConfiguredJSONDepth(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxItemDepth = 3
	body := []byte(`{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"x"}]}]}`)
	if _, _, err := DecodeRequest(body, limits); err == nil {
		t.Fatal("expected configured JSON depth limit to reject nested request")
	} else if !strings.Contains(err.Error(), "exceeds limit 3") {
		t.Fatalf("error = %v, want configured depth limit", err)
	}
}

func assertLimitParam(t *testing.T, err error, param string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s limit error", param)
	}
	var limitErr *LimitExceededError
	if !errors.As(err, &limitErr) {
		t.Fatalf("error = %v, want LimitExceededError", err)
	}
	if limitErr.Param != param {
		t.Fatalf("limit param = %q, want %q", limitErr.Param, param)
	}
}
