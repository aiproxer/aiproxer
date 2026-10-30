package downstreamcacheaffinity

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/stretchr/testify/require"
)

const (
	vectorBackendID = "mistral-chat"
	vectorSessionID = "9f8c1e2a4b6d70a31c5e88f2d4b60a9c7e31d5f8026b4c79ad3e5106f2b8c47"
	// Independently computed SHA-256/base64url vector, not a mirrored oracle.
	vectorValue = "aipca1_pe4D5OccDTMvRTOPFb5dwaFTwxCA-dGbK6SpvidFppg"
)

func TestDerive_VectorAlphabetAndSeparation(t *testing.T) {
	require.Equal(t, vectorValue, Derive(vectorBackendID, vectorSessionID))
	seen := map[string]bool{}
	for _, input := range [][2]string{
		{vectorBackendID, vectorSessionID},
		{"deepinfra-chat", vectorSessionID},
		{vectorBackendID, "other-session"},
		{"", ""},
		{"беckend-ü", "сессия-🔑"},
		{"a\x00b", "c\x00d"},
		{strings.Repeat("b", 512), strings.Repeat("s", 1024)},
	} {
		got := Derive(input[0], input[1])
		require.Equal(t, got, Derive(input[0], input[1]))
		require.Len(t, got, 50)
		require.Regexp(t, `^aipca1_[A-Za-z0-9_-]{43}$`, got)
		require.False(t, seen[got], "different backend/session collided")
		seen[got] = true
	}
}

func carrier(value string) aipapi.SemanticExtension {
	data, _ := json.Marshal(value)
	return aipapi.SemanticExtension{
		Namespace: "aip", Type: "prompt_cache_key", Implementor: "proxy",
		Direction: aipapi.SemanticExtensionDirectionRequest, Presence: aipapi.SemanticExtensionValue, Data: data,
	}
}

func TestTransform_FillOnlyAndContinue(t *testing.T) {
	tr := Transform{}
	require.Equal(t, "downstream-cache-affinity", tr.ID())
	require.Greater(t, tr.Order(), 0)
	require.Equal(t, hooks.FailOpen, tr.FailureMode())
	m := request.AttemptMeta{}
	m.Session.ClientSessionHint = "client-hint"
	m.ALegID, m.TraceID, m.CandidateKey, m.Model = "aleg", "trace", "candidate", "model"
	m.Scope.PrincipalID.Value, m.Scope.OrganizationID.Value = "principal", "org"
	m.Scope.SafeClaims = map[string]string{"email": "b@example.com"}
	m.Workspace.ID, m.Workspace.ProjectRoot = "workspace", "/srv/other"
	m.Session.WorkspaceID, m.Session.TurnID = "ws", "turn"
	m.Session.Labels = map[string]string{"label": "v"}
	fixture := aipapi.Call{
		ID: "request", Route: aipapi.RouteIntent{Selector: "model"},
		Messages:   []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
		Session:    aipapi.SessionRef{ClientSessionID: "client", ALegID: "aleg", Metadata: map[string]string{"k": "v"}},
		Invocation: aipapi.Invocation{ClientUserAgent: "codex/1.0"}, Extensions: map[string]json.RawMessage{"x": json.RawMessage(`{"a":1}`)}, PreviousResponseID: "resp",
	}
	for _, tc := range []struct {
		name                   string
		call                   *aipapi.Call
		backend, session, want string
	}{
		{"nil", nil, vectorBackendID, vectorSessionID, ""},
		{"generated", &aipapi.Call{}, vectorBackendID, vectorSessionID, vectorValue},
		{"other_candidate", &aipapi.Call{}, "deepinfra-chat", vectorSessionID, Derive("deepinfra-chat", vectorSessionID)},
		{"first_candidate_again", &aipapi.Call{}, vectorBackendID, vectorSessionID, vectorValue},
		{"legacy", &aipapi.Call{PromptCacheKey: "client"}, vectorBackendID, vectorSessionID, "client"},
		{"carrier", &aipapi.Call{SemanticExtensions: []aipapi.SemanticExtension{carrier("client")}}, vectorBackendID, vectorSessionID, ""},
		{"matching", &aipapi.Call{PromptCacheKey: "client", SemanticExtensions: []aipapi.SemanticExtension{carrier("client")}}, vectorBackendID, vectorSessionID, "client"},
		{"conflict", &aipapi.Call{PromptCacheKey: "legacy", SemanticExtensions: []aipapi.SemanticExtension{carrier("other")}}, vectorBackendID, vectorSessionID, "legacy"},
		{"client_hint_only", &aipapi.Call{}, vectorBackendID, "", ""},
		{"blank_session", &aipapi.Call{}, vectorBackendID, " \t\n", ""},
		{"no_backend", &aipapi.Call{}, "", vectorSessionID, ""},
		{"blank_backend", &aipapi.Call{}, "  ", vectorSessionID, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var before *aipapi.Call
			if tc.call != nil {
				call := aipapi.CloneCall(fixture)
				call.PromptCacheKey, call.SemanticExtensions = tc.call.PromptCacheKey, tc.call.SemanticExtensions
				tc.call = &call
				clone := aipapi.CloneCall(call)
				before = &clone
				before.PromptCacheKey = tc.want
			}
			m.BackendID, m.Session.AuthoritativeSessionID = tc.backend, tc.session
			d, err := tr.HandleAttempt(t.Context(), tc.call, m, request.Services{})
			require.NoError(t, err)
			require.Equal(t, request.AttemptDecision{Kind: request.AttemptContinue}, d)
			require.Equal(t, before, tc.call, "only an empty PCK may change")
		})
	}
}
