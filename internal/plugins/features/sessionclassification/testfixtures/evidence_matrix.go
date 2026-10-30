// Package testfixtures exposes the shared, data-driven evidence matrix used by
// session-classification acceptance tests. It contains no classification
// policy; tool categories are checked by consumers through aipapi.
package testfixtures

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	ClassificationUnknown     = "unknown"
	ClassificationCodingAgent = "coding_agent"

	IdentityHighConfidence              = "high_confidence"
	IdentityAmbiguous                   = "ambiguous"
	IdentityUnsupportedByUserAgentAlone = "unsupported_by_user_agent_alone"
)

// Matrix freezes the evidence and identity support table for later acceptance
// tests without embedding a second classifier in this package.
type Matrix struct {
	SchemaVersion          int               `json:"schemaVersion"`
	Sources                []Source          `json:"sources"`
	HarnessIdentitySupport []HarnessIdentity `json:"harnessIdentitySupport"`
	Fixtures               []EvidenceFixture `json:"fixtures"`
}

type Source struct {
	ID          string `json:"id"`
	Reference   string `json:"reference"`
	Description string `json:"description"`
}

type HarnessIdentity struct {
	Harness       string   `json:"harness"`
	Status        string   `json:"status"`
	IdentityField string   `json:"identityField"`
	Examples      []string `json:"examples"`
	SourceRefs    []string `json:"sourceRefs"`
	Reason        string   `json:"reason"`
}

type EvidenceFixture struct {
	ID                       string         `json:"id"`
	Harness                  string         `json:"harness,omitempty"`
	IdentityField            string         `json:"identityField,omitempty"`
	IdentityValue            string         `json:"identityValue,omitempty"`
	ToolEvidence             []ToolEvidence `json:"toolEvidence,omitempty"`
	ProjectMarkers           []string       `json:"projectMarkers,omitempty"`
	WeakSignals              []string       `json:"weakSignals,omitempty"`
	WeakPrompt               string         `json:"weakPrompt,omitempty"`
	ModelName                string         `json:"modelName,omitempty"`
	IgnoredUserAgentPrefixes []string       `json:"ignoredUserAgentPrefixes,omitempty"`
	PriorClassification      string         `json:"priorClassification,omitempty"`
	ExpectedClassification   string         `json:"expectedClassification"`
	ExpectedEvidenceCodes    []string       `json:"expectedEvidenceCodes"`
	SourceRefs               []string       `json:"sourceRefs"`
	Reason                   string         `json:"reason"`
}

type ToolEvidence struct {
	Name             string `json:"name"`
	ExpectedCategory string `json:"expectedCategory"`
	Description      string `json:"description,omitempty"`
}

//go:embed testdata/evidence_matrix.json
var evidenceMatrixJSON []byte

// Load decodes the embedded matrix so tests in other packages can consume the
// same artifact independent of their working directory.
func Load() (Matrix, error) {
	var matrix Matrix
	decoder := json.NewDecoder(bytes.NewReader(evidenceMatrixJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&matrix); err != nil {
		return Matrix{}, fmt.Errorf("decode evidence matrix: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return Matrix{}, fmt.Errorf("decode evidence matrix: trailing JSON value")
		}
		return Matrix{}, fmt.Errorf("decode evidence matrix trailing data: %w", err)
	}
	return matrix, nil
}
