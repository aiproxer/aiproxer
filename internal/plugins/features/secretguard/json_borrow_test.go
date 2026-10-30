package secretguard

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

func TestGuard_JSONBackingBytesRemainUnchangedAcrossDecisions(t *testing.T) {
	shared := json.RawMessage(` {"value": "` + adapterGitHubToken + `"} `)
	original := bytes.Clone(shared)
	generation := newProvenanceBetterLeaksServices(t)
	for _, action := range []string{ActionBlock, ActionLog, ActionRedact} {
		for _, failure := range []string{"none", "limit", "matcher"} {
			if action != ActionRedact && failure != "none" {
				continue
			}
			t.Run(action+"/"+failure, func(t *testing.T) {
				t.Parallel()
				call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartJSON, Content: shared}, aipapi.TextPart("tail")}}}}
				before := aipapi.CloneCall(call)
				cfg := Config{Action: action}
				var matcher sdk.Matcher = newExactStub(adapterGitHubToken, "SECRET", sdk.SourceCategoryProxyEnv)
				switch failure {
				case "limit":
					cfg.ScanMaxBytes = len(shared)
				case "matcher":
					matcher = &failingRedactMatcher{match: "tail", err: errors.New("fixture error")}
				}
				d, err := NewGuard(cfg).Evaluate(t.Context(), &call, sdk.Meta{}, provenanceServices("hybrid", matcher, generation))
				if !bytes.Equal(shared, original) {
					t.Fatal("guard changed a backing buffer shared with another request")
				}
				if failure == "matcher" && err == nil || failure != "matcher" && (err != nil || d.Validate() != nil) {
					t.Fatal("guard lost its decision/error contract")
				}
				if action != ActionRedact || failure != "none" {
					if !reflect.DeepEqual(call, before) {
						t.Fatal("read-only or unsuccessful redaction changed the canonical call")
					}
				} else if d.Outcome != sdk.OutcomeRedacted || bytes.Contains(call.Messages[0].Parts[0].Content, []byte(adapterGitHubToken)) {
					t.Fatal("successful redaction did not publish masked JSON")
				}
			})
		}
	}
}
