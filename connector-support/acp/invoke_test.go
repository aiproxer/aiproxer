package acp

import (
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestValidateACPCall_rejectsTools(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		ID: "x",
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
		Tools: []aipapi.ToolDef{{Name: "n", Parameters: json.RawMessage(`{}`)}},
	}
	if err := validateACPCall(&call); err == nil {
		t.Fatal("expected error")
	}
}

func TestSessionIDFromExtensions(t *testing.T) {
	t.Parallel()
	raw, _ := json.Marshal("sess_ref_42")
	call := aipapi.Call{
		ID: "x",
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
		Extensions: map[string]json.RawMessage{
			extSessionJSONKey: raw,
		},
	}
	if sessionIDFromExtensions(&call) != "sess_ref_42" {
		t.Fatal("session id")
	}
}

func TestPromptBlocksForCall_textAndResource(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		ID: "x",
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{
				aipapi.TextPart("hi"),
				{Kind: aipapi.PartFileRef, FileRef: "file:///tmp/a.py", FileMIME: "text/x-python", FileName: "x"},
			},
		}},
	}
	blocks, err := promptBlocksForCall(&call)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) < 2 {
		t.Fatalf("blocks: %#v", blocks)
	}
	if blocks[1]["type"] != "resource" {
		t.Fatalf("expected resource block: %#v", blocks[1])
	}
}
