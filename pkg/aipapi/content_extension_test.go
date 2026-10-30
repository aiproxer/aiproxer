package aipapi_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func extContentPart(typ, data string) aipapi.ContentPart {
	return aipapi.ContentPart{
		Kind: aipapi.ContentPartExtension,
		Extension: &aipapi.ExtensionContentPart{
			Type: typ,
			Data: json.RawMessage(data),
		},
	}
}

func itemWith(parts ...aipapi.ContentPart) aipapi.Item {
	return aipapi.Item{
		Kind:    aipapi.ItemKindMessage,
		ID:      "item-1",
		Status:  aipapi.ItemStatusCompleted,
		Role:    aipapi.RoleUser,
		Content: parts,
	}
}

func callWith(items ...aipapi.Item) aipapi.Call {
	return aipapi.Call{ID: "call-1", Items: items}
}

func TestContentPartExtension_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		part    aipapi.ContentPart
		wantErr string
	}{
		{
			name: "valid prefixed colon extension",
			part: extContentPart("acme:input_file", `{"type":"acme:input_file","file_url":"https://x/f"}`),
		},
		{
			name: "valid prefixed slash extension",
			part: extContentPart("acme.com/part", `{"type":"acme.com/part","payload":{"k":1}}`),
		},
		{
			name:    "missing type",
			part:    extContentPart("", `{"type":"acme:input_file"}`),
			wantErr: "extension type is required",
		},
		{
			name:    "unprefixed type rejected",
			part:    extContentPart("invented_type", `{"type":"invented_type"}`),
			wantErr: "vendor-prefixed",
		},
		{
			name:    "missing extension record",
			part:    aipapi.ContentPart{Kind: aipapi.ContentPartExtension},
			wantErr: "requires Extension",
		},
		{
			name:    "missing data",
			part:    extContentPart("acme:part", ""),
			wantErr: "requires Data",
		},
		{
			name:    "invalid json data",
			part:    extContentPart("acme:part", `{not json`),
			wantErr: "must be valid JSON",
		},
		{
			name:    "data type mismatch",
			part:    extContentPart("acme:part", `{"type":"other:part"}`),
			wantErr: "must match Type",
		},
		{
			name:    "non-object data rejected",
			part:    extContentPart("acme:part", `[1,2]`),
			wantErr: "must be a JSON object",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			call := callWith(itemWith(tt.part))
			err := call.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected validation error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tt.wantErr, err)
			}
		})
	}
}

func TestContentPartExtension_BoundedData(t *testing.T) {
	t.Parallel()
	// Size bound is enforced before JSON parsing, so an oversized raw payload is
	// rejected without allocation-heavy JSON validation.
	part := extContentPart("acme:part", strings.Repeat(" ", aipapi.MaxExtensionDataBytes+1))
	if err := callWith(itemWith(part)).Validate(); err == nil {
		t.Fatal("expected oversized extension data rejection")
	}
}

func TestContentPartFileRef_RequiresRefOrData(t *testing.T) {
	t.Parallel()
	if err := callWith(itemWith(aipapi.ContentPart{Kind: aipapi.ContentPartFileRef})).Validate(); err == nil {
		t.Fatal("expected error for empty FileRef and FileData")
	}
	if err := callWith(itemWith(aipapi.ContentPart{Kind: aipapi.ContentPartFileRef, FileRef: "https://x/f.pdf", FileData: "aGVsbG8=", FileName: "f.pdf"})).Validate(); err != nil {
		t.Fatalf("unexpected error for ref+data file part: %v", err)
	}
	if err := callWith(itemWith(aipapi.ContentPart{Kind: aipapi.ContentPartFileRef, FileData: "aGVsbG8=", FileName: "f.pdf"})).Validate(); err != nil {
		t.Fatalf("unexpected error for data-only file part: %v", err)
	}
}

func TestContentPartExtension_ClonePreservesData(t *testing.T) {
	t.Parallel()
	call := callWith(itemWith(aipapi.ContentPart{
		Kind: aipapi.ContentPartExtension,
		Extension: &aipapi.ExtensionContentPart{
			Namespace:   "acme",
			Type:        "acme:input_file",
			Implementor: "acme-vendor",
			Data:        json.RawMessage(`{"type":"acme:input_file","file_url":"https://x/f"}`),
		},
	}))
	cloned := aipapi.CloneCall(call)
	orig := call.Items[0].Content[0].Extension
	got := cloned.Items[0].Content[0].Extension
	if got == nil || got.Type != orig.Type || got.Namespace != orig.Namespace || got.Implementor != orig.Implementor || string(got.Data) != string(orig.Data) {
		t.Fatalf("clone did not preserve extension content part: %+v vs %+v", got, orig)
	}
	got.Data[0] = ' '
	if string(call.Items[0].Content[0].Extension.Data) == string(got.Data) {
		t.Fatal("clone shares extension data backing array")
	}
}

func TestContentPartExtension_WalkOpaqueData(t *testing.T) {
	t.Parallel()
	call := callWith(itemWith(extContentPart("acme:meta", `{"type":"acme:meta","v":1}`)))
	var fields []string
	if err := aipapi.WalkCallOpaqueData(call, func(field string, data []byte) error {
		fields = append(fields, field+"="+string(data))
		return nil
	}); err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(fields) != 1 || !strings.Contains(fields[0], "acme:meta") {
		t.Fatalf("opaque walk did not visit extension content data: %v", fields)
	}
}

func TestContentPartExtension_RequiredCapabilities(t *testing.T) {
	t.Parallel()
	call := callWith(itemWith(extContentPart("acme:part", `{"type":"acme:part"}`)))
	req := aipapi.DeriveProtocolRequirements(call)
	if !slices.Contains(req.Capabilities, aipapi.CapabilityOpaqueExtensions) {
		t.Fatalf("expected CapabilityOpaqueExtensions in requirements, got %v", req.Capabilities)
	}
	required := aipapi.RequiredCapabilities(call)
	if !slices.Contains(required, aipapi.CapabilityOpaqueExtensions) {
		t.Fatalf("expected CapabilityOpaqueExtensions in RequiredCapabilities, got %v", required)
	}
}

func TestContentPartExtension_RequiredCapabilitiesFromToolResult(t *testing.T) {
	t.Parallel()
	call := callWith(aipapi.Item{
		Kind:   aipapi.ItemKindToolResult,
		ID:     "tr-1",
		Status: aipapi.ItemStatusCompleted,
		ToolResult: &aipapi.ToolResultItem{
			CallID: "call-1",
			Name:   "lookup",
			Parts:  []aipapi.ContentPart{extContentPart("acme:part", `{"type":"acme:part"}`)},
		},
	})
	required := aipapi.RequiredCapabilities(call)
	if !slices.Contains(required, aipapi.CapabilityOpaqueExtensions) {
		t.Fatalf("expected CapabilityOpaqueExtensions in RequiredCapabilities for tool-result extension part, got %v", required)
	}
	req := aipapi.DeriveProtocolRequirements(call)
	if !slices.Contains(req.Capabilities, aipapi.CapabilityOpaqueExtensions) {
		t.Fatalf("expected CapabilityOpaqueExtensions in requirements for tool-result extension part, got %v", req.Capabilities)
	}
}

func TestContentPartExtension_LegacyProjectionRejects(t *testing.T) {
	t.Parallel()
	call := callWith(itemWith(extContentPart("acme:part", `{"type":"acme:part"}`)))
	target := aipapi.LegacyProjectionTargetFromCaps(aipapi.NewBackendCaps(aipapi.CapabilityOrderedItems, aipapi.CapabilityOpaqueExtensions), aipapi.ReasoningReplaySupport{})
	if _, err := aipapi.ProjectItemsToLegacyView(call, target); err == nil {
		t.Fatal("expected legacy projection to reject opaque extension content part")
	} else if !aipapi.IsProjectionError(err) {
		t.Fatalf("expected ProjectionError, got: %T %v", err, err)
	}
}

func TestContentPartFileData_ValidationBounds(t *testing.T) {
	t.Parallel()
	part := aipapi.ContentPart{Kind: aipapi.ContentPartFileRef, FileData: strings.Repeat("a", aipapi.MaxPartTextBytes+1)}
	if err := callWith(itemWith(part)).Validate(); err == nil {
		t.Fatal("expected oversized FileData rejection")
	}
}
