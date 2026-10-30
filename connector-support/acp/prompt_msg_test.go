package acp

import (
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestBuildPromptParams_includesMessageID(t *testing.T) {
	t.Parallel()
	p := buildPromptParams("sid", []map[string]any{{"type": "text", "text": "a"}}, "mid-1")
	if p["messageId"] != "mid-1" {
		t.Fatalf("%#v", p)
	}
}

func TestMessageIDForCall_fromExtension(t *testing.T) {
	t.Parallel()
	raw, _ := json.Marshal("fixed-id")
	call := aipapi.Call{
		ID: "x",
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
		Extensions: map[string]json.RawMessage{extMessageIDJSONKey: raw},
	}
	if messageIDForCall(&call) != "fixed-id" {
		t.Fatal(messageIDForCall(&call))
	}
}

func TestMessageIDForCall_fallbackIsDeterministic(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	want := "msg_" + stableCallToken(&call)
	if got := messageIDForCall(&call); got != want {
		t.Fatalf("messageIDForCall() = %q, want %q", got, want)
	}
}
