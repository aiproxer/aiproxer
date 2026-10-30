package secretguard

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/metering/checkpoint"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

func TestGuard_CleanJSONPreservesCanonicalBytes(t *testing.T) {
	generation := newProvenanceBetterLeaksServices(t)
	for _, detector := range []string{"exact", "hybrid"} {
		for _, authority := range []string{"messages", "items"} {
			for _, raw := range []string{` {"b": 2, "a": 1} `, `{"a":1,"a":2}`, `{"s":"\u0061\/b"}`} {
				t.Run(detector+authority+raw, func(t *testing.T) {
					call := baseCall()
					call.Messages[0].Parts = []aipapi.Part{{Kind: aipapi.PartJSON, Content: json.RawMessage(raw)}}
					if authority == "items" {
						call.Messages, call.Instructions = nil, nil
						call.Items = []aipapi.Item{{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartJSON, Text: raw}}}}
					}
					before := aipapi.CloneCall(call)
					d, err := NewGuard(mustCfg(t, ActionRedact)).Evaluate(t.Context(), &call, sdk.Meta{}, provenanceServices(detector, newExactStub("not-present", "SECRET", sdk.SourceCategoryProxyEnv), generation))
					if err != nil || d.Outcome != sdk.OutcomePass || d.MutationCount != 0 || len(d.Findings) != 0 || d.Validate() != nil || !reflect.DeepEqual(call, before) {
						t.Fatalf("clean JSON changed or failed: decision=%+v err=%v", d, err)
					}
					block, err := extensions.RunSecretGuardStage(t.Context(), nil, nil, []sdk.Guard{NewGuard(mustCfg(t, ActionRedact))}, &call, sdk.Meta{}, provenanceServices(detector, newExactStub("not-present", "SECRET", sdk.SourceCategoryProxyEnv), generation), nil, nil)
					if err != nil || block != nil || !reflect.DeepEqual(call, before) {
						t.Fatal("clean JSON must pass runtime decision validation unchanged")
					}
					if authority == "items" {
						call.Items[0].Content = append(call.Items[0].Content, aipapi.ContentPart{Kind: aipapi.ContentPartText, Text: "not-present"})
					} else {
						call.Messages[0].Parts = append(call.Messages[0].Parts, aipapi.TextPart("not-present"))
					}
					d, err = NewGuard(mustCfg(t, ActionRedact)).Evaluate(t.Context(), &call, sdk.Meta{}, provenanceServices(detector, newExactStub("not-present", "SECRET", sdk.SourceCategoryProxyEnv), generation))
					if err != nil || d.Outcome != sdk.OutcomeRedacted || d.MutationCount != 1 || d.Validate() != nil {
						t.Fatal("redaction in another fragment normalized clean JSON")
					}
					if authority == "items" && call.Items[0].Content[0].Text != raw || authority == "messages" && string(call.Messages[0].Parts[0].Content) != raw {
						t.Fatal("clean JSON bytes changed during another fragment's redaction")
					}
				})
			}
		}
	}
}

type errorResolver struct{ err error }

func (r errorResolver) Resolve(context.Context) (sdk.Matcher, error) { return nil, r.err }

func TestGuard_MatcherResolutionFailureSurfacesError(t *testing.T) {
	t.Parallel()

	call := baseCall()
	before := aipapi.CloneCall(call)
	wantErr := errors.New("matcher resolver unavailable")

	_, err := NewGuard(mustCfg(t, ActionRedact)).Evaluate(t.Context(), &call, sdk.Meta{}, sdk.Services{MatcherResolver: errorResolver{err: wantErr}})
	if !errors.Is(err, wantErr) {
		t.Fatalf("resolver failure must surface as an error, got %v", err)
	}
	if !reflect.DeepEqual(call, before) {
		t.Fatal("resolver failure must leave the call unchanged")
	}
}

func TestRedactJSONPayload_UnchangedFindingPreservesBytes(t *testing.T) {
	m := newRecordingJSONMatcher()
	m.redactions["unchanged"] = "unchanged"
	m.redactHits["unchanged"] = findingForRef("SECRET")
	raw := ` {"value": "unchanged"} `
	out, findings, err := redactJSONPayload(t.Context(), m, []byte(raw))
	if err != nil || string(out) != raw || len(findings) != 1 {
		t.Fatal("findings without replacements must not normalize JSON")
	}
}

func TestGuard_JSONMirrorRedactionSurvivesCheckpoint(t *testing.T) {
	raw := `{"value":"redact-me"}`
	call := baseCall()
	call.Messages = nil
	call.Instructions = nil
	call.Items = []aipapi.Item{{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartJSON, Text: raw, Annotation: &aipapi.AnnotationPart{Type: "json_content", Data: json.RawMessage(raw)}}}}}
	if err := call.Validate(); err != nil {
		t.Fatal(err)
	}
	d, err := NewGuard(mustCfg(t, ActionRedact)).Evaluate(t.Context(), &call, sdk.Meta{}, servicesWith(newExactStub("redact-me", "SECRET", sdk.SourceCategoryProxyEnv)))
	if err != nil || d.Outcome != sdk.OutcomeRedacted || d.Validate() != nil {
		t.Fatalf("redaction failed: decision=%+v err=%v", d, err)
	}
	snap, err := checkpoint.CaptureFrontendIngress(checkpoint.FrontendIngressInput{CheckpointID: "post-guard", Call: call})
	if err != nil {
		t.Fatal(err)
	}
	part := snap.Call.Items[0].Content[0]
	if part.Text != `{"value":"*********"}` || string(part.Annotation.Data) != part.Text {
		t.Fatal("checkpoint retained an unredacted JSON mirror")
	}
	before := aipapi.CloneCall(call)
	call.Items[0].Content[0].Annotation.Data = json.RawMessage(`{"value":"different"}`)
	inconsistent := aipapi.CloneCall(call)
	_, err = NewGuard(mustCfg(t, ActionRedact)).Evaluate(t.Context(), &call, sdk.Meta{}, servicesWith(newExactStub("different", "SECRET", sdk.SourceCategoryProxyEnv)))
	if err == nil || strings.Contains(err.Error(), "different") || !reflect.DeepEqual(call, inconsistent) {
		t.Fatal("inconsistent mirror must fail safely without mutation")
	}
	if string(before.Items[0].Content[0].Annotation.Data) != before.Items[0].Content[0].Text {
		t.Fatal("checkpoint and canonical clones must own their mirrors")
	}
}
