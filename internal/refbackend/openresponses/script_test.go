package openresponses

import (
	"strings"
	"testing"
	"time"
)

func validTextResource() *Resource {
	return NewResource("resp_ok", "gpt-openresponses-1", 1719900000, []Item{
		NewMessageItem("assistant", "output_text", "hello"),
	})
}

// TestScriptValidate_AcceptsEveryMode proves each declared mode validates with a
// non-empty description and a buildable resource.
func TestScriptValidate_AcceptsEveryMode(t *testing.T) {
	t.Parallel()
	for _, mode := range []Mode{ModeJSON, ModeSSE, ModeCompact, ModeWebSocket} {
		sc := &Script{ID: "scenario-good", Description: "ok", Mode: mode, Resource: validTextResource()}
		if mode == ModeCompact {
			sc.Resource = nil
			sc.CompactResource = NewCompactResource("resp_c", "m", 1, []Item{NewCompactionItem("cmp_1", "")})
		}
		if err := sc.Validate(); err != nil {
			t.Errorf("mode %s: %v", mode, err)
		}
	}
}

func TestScriptValidate_RejectsEmptyIDAndDescription(t *testing.T) {
	t.Parallel()
	cases := []*Script{
		{ID: "", Description: "x", Mode: ModeJSON, Resource: validTextResource()},
		{ID: "Bad_ID", Description: "x", Mode: ModeJSON, Resource: validTextResource()},
		{ID: "scenario-x", Description: "   ", Mode: ModeJSON, Resource: validTextResource()},
	}
	for _, sc := range cases {
		if err := sc.Validate(); err == nil {
			t.Errorf("expected validation error for %+v", sc)
		}
	}
}

func TestScriptValidate_RejectsUnknownModeAndMalformed(t *testing.T) {
	t.Parallel()
	sc := &Script{ID: "scenario-x", Description: "x", Mode: Mode("bogus"), Resource: validTextResource()}
	if err := sc.Validate(); err == nil {
		t.Fatal("expected unknown-mode error")
	}
	bad := &Script{ID: "scenario-x", Description: "x", Mode: ModeSSE, Resource: validTextResource(), Malformed: MalformedMode("mystery")}
	if err := bad.Validate(); err == nil {
		t.Fatal("expected unknown-malformed error")
	}
}

func TestScriptValidate_RejectsNegativeBounds(t *testing.T) {
	t.Parallel()
	cases := []*Script{
		{ID: "scenario-x", Description: "x", Mode: ModeJSON, Resource: validTextResource(), DisconnectAfter: -1},
		{ID: "scenario-x", Description: "x", Mode: ModeJSON, Resource: validTextResource(), Delay: DelayPlan{BeforeFirst: -time.Second}},
		{ID: "scenario-x", Description: "x", Mode: ModeJSON, Resource: validTextResource(), Expected: ExpectedRequest{MinInputItems: -1}},
	}
	for _, sc := range cases {
		if err := sc.Validate(); err == nil {
			t.Errorf("expected negative-bound error for %+v", sc)
		}
	}
}

func TestScriptValidate_ErrorStepRequiresStatusAndFields(t *testing.T) {
	t.Parallel()
	good := &Script{
		ID: "scenario-err", Description: "x", Mode: ModeJSON, Resource: validTextResource(),
		Error: &ErrorStep{Status: 429, Type: "requests", Code: "rate_limit_exceeded", Message: "slow down", RetryAfter: "5"},
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("good error script rejected: %v", err)
	}
	bad := &Script{
		ID: "scenario-err", Description: "x", Mode: ModeJSON, Resource: validTextResource(),
		Error: &ErrorStep{Status: 200, Type: "x", Code: "y", Message: "z"},
	}
	if err := bad.Validate(); err == nil {
		t.Fatal("expected 2xx error-step rejection")
	}
	missing := &Script{
		ID: "scenario-err", Description: "x", Mode: ModeJSON, Resource: validTextResource(),
		Error: &ErrorStep{Status: 500, Type: "", Code: "y", Message: "z"},
	}
	if err := missing.Validate(); err == nil {
		t.Fatal("expected missing type rejection")
	}
}

func TestScriptValidate_RejectsSuccessWithoutBody(t *testing.T) {
	t.Parallel()
	sc := &Script{ID: "scenario-x", Description: "x", Mode: ModeSSE}
	if err := sc.Validate(); err == nil {
		t.Fatal("expected missing-resource rejection")
	}
	// MalformedBodyNotJSON is the documented exception.
	sc = &Script{ID: "scenario-x", Description: "x", Mode: ModeJSON, Malformed: MalformedBodyNotJSON}
	if err := sc.Validate(); err != nil {
		t.Fatalf("body-not-json exception rejected: %v", err)
	}
}

func TestScriptValidate_ExplicitSSEWithoutResource(t *testing.T) {
	t.Parallel()
	sc := &Script{
		ID: "scenario-steps", Description: "x", Mode: ModeSSE,
		SSE: []WireStep{{Type: "response.created", Sequence: 0, Data: []byte(`{"response":{"id":"r"}}`)}},
	}
	if err := sc.Validate(); err != nil {
		t.Fatalf("explicit steps rejected: %v", err)
	}
}

func TestScriptValidate_MinExceedsMax(t *testing.T) {
	t.Parallel()
	sc := &Script{
		ID: "scenario-x", Description: "x", Mode: ModeJSON, Resource: validTextResource(),
		Expected: ExpectedRequest{MinInputItems: 5, MaxInputItems: 2},
	}
	if err := sc.Validate(); err == nil || !strings.Contains(err.Error(), "min input items exceeds max") {
		t.Fatalf("expected min>max error, got %v", err)
	}
}

func TestScriptValidate_CompactCannotAssertStream(t *testing.T) {
	t.Parallel()
	stream := true
	sc := &Script{
		ID: "scenario-x", Description: "x", Mode: ModeCompact,
		CompactResource: NewCompactResource("c", "m", 1, nil),
		Expected:        ExpectedRequest{Stream: &stream},
	}
	if err := sc.Validate(); err == nil {
		t.Fatal("expected compact stream assertion rejection")
	}
}

func TestServer_RegisterRejectsDuplicateAndInvalid(t *testing.T) {
	t.Parallel()
	srv := NewServer(Options{AllowMissingBearer: true})
	sc := &Script{ID: "scenario-a", Description: "a", Mode: ModeJSON, Resource: validTextResource()}
	if err := srv.Register(sc); err != nil {
		t.Fatal(err)
	}
	if err := srv.Register(sc); err == nil {
		t.Fatal("expected duplicate registration error")
	}
	if err := srv.Register(&Script{ID: "bad", Description: "x", Mode: ModeJSON}); err == nil {
		t.Fatal("expected invalid registration error")
	}
	if err := srv.Select("missing"); err != ErrUnknownScript {
		t.Fatalf("expected ErrUnknownScript, got %v", err)
	}
	if err := srv.Select("scenario-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.ActiveScript(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewServer(Options{AllowMissingBearer: true}).ActiveScript(); err != ErrNoScriptSelected {
		t.Fatalf("expected ErrNoScriptSelected, got %v", err)
	}
}
