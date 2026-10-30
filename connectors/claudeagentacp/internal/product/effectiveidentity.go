package product

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"

	"github.com/aiproxer/aiproxer/connector-support/acp"
)

var _ acp.PromptAdmissionSpec = (*ClaudeSpec)(nil)

type effectiveIdentity struct {
	Version             int    `json:"version"`
	SessionID           string `json:"sessionId"`
	CheckpointID        string `json:"checkpointId"`
	ProcessGeneration   string `json:"processGeneration"`
	QueryGeneration     uint64 `json:"queryGeneration"`
	ObservationSequence uint64 `json:"observationSequence"`
	Provider            string `json:"provider"`
	CredentialSource    string `json:"credentialSource"`
}

func (*ClaudeSpec) PromptAdmissionMethod() string      { return "_aip/effective_identity_v1" }
func (*ClaudeSpec) PromptAdmissionMetadataKey() string { return "aipIdentity" }

// ValidatePromptAdmission is deliberately private-wire validation, not a CLI
// credential reader. The previous observation belongs to this transport only.
func (s *ClaudeSpec) ValidatePromptAdmission(sessionID, checkpointID string, previous, response json.RawMessage) error {
	refuse := errors.New("claudeagentacp: effective query identity refused")
	current, err := decodeEffectiveIdentity(response)
	if err != nil || current.SessionID != sessionID || current.CheckpointID != checkpointID || current.CredentialSource != string(s.auth.source) {
		return refuse
	}
	if len(previous) != 0 {
		prior, err := decodeEffectiveIdentity(previous)
		if err != nil || prior.SessionID != current.SessionID || prior.ProcessGeneration != current.ProcessGeneration || prior.QueryGeneration != current.QueryGeneration || prior.ObservationSequence >= current.ObservationSequence || prior.CheckpointID == current.CheckpointID || prior.CredentialSource != current.CredentialSource {
			return refuse
		}
	}
	return nil
}

func decodeEffectiveIdentity(raw json.RawMessage) (effectiveIdentity, error) {
	refuse := errors.New("invalid effective identity tuple")
	var result effectiveIdentity
	if len(raw) > 2048 {
		return result, refuse
	}
	// Decode keys explicitly: encoding/json's usual struct decoder accepts
	// duplicates and case-insensitive names, neither belongs to this exact wire.
	d := json.NewDecoder(bytes.NewReader(raw))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return result, refuse
	}
	fields := map[string]any{
		"version": &result.Version, "sessionId": &result.SessionID, "checkpointId": &result.CheckpointID,
		"processGeneration": &result.ProcessGeneration, "queryGeneration": &result.QueryGeneration,
		"observationSequence": &result.ObservationSequence, "provider": &result.Provider, "credentialSource": &result.CredentialSource,
	}
	seen := make(map[string]bool, len(fields))
	for d.More() {
		key, err := d.Token()
		name, ok := key.(string)
		if err != nil || !ok || fields[name] == nil || seen[name] {
			return result, refuse
		}
		seen[name] = true
		if err := d.Decode(fields[name]); err != nil {
			return result, refuse
		}
	}
	if end, err := d.Token(); err != nil || end != json.Delim('}') || len(seen) != len(fields) {
		return result, refuse
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return result, refuse
	}
	if result.Version != 1 || result.SessionID == "" || len(result.SessionID) > 256 || !identityNonce(result.CheckpointID) || !identityNonce(result.ProcessGeneration) || result.QueryGeneration == 0 || result.QueryGeneration > 1<<53-1 || result.ObservationSequence == 0 || result.ObservationSequence > 1<<53-1 || result.Provider != "firstParty" {
		return result, refuse
	}
	switch result.CredentialSource {
	case "api_key", "subscription_token", "host_login", "gateway":
	default:
		return result, refuse
	}
	return result, nil
}

func identityNonce(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if c >= 'A' && c <= 'F' {
			return false
		}
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
