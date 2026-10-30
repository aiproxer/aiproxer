package openresponses

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Profile identity constants for official OpenResponses 2026-04-24 specification.
const (
	ProtocolFamily           = "openresponses"
	ProfileVersion           = "2026-04-24"
	SourceCommit             = "92c12d96d7b61d6d15e2214daa5e9c6000ab6e1c"
	SourceRepository         = "openresponses/openresponses"
	LicenseName              = "Apache-2.0"
	LicenseAttribution       = "Licensed under the Apache License, Version 2.0"
	DefaultBasePath          = "/openresponses/v1"
	ExpectedSchemaDigest     = "sha256:997c4cf16c349751502813f46ea79b2c88880b23171b69f7f2c3d4bf5b330529"
	ExpectedComplianceDigest = "sha256:63b5e6595ac831ee74b8e887af76c28d69aee8e2ec7d9e99dc688eec4bccb7fb"
	ExpectedLicenseDigest    = "sha256:43070e2d4e532684de521b885f385d0841030efa2b1a20bafb76133a5e1379c1"
)

// ProfileDescriptor defines the immutable protocol profile evidence and metadata.
type ProfileDescriptor struct {
	Family             string   `json:"family"`
	Version            string   `json:"version"`
	SourceCommit       string   `json:"source_commit"`
	SourceRepository   string   `json:"source_repository"`
	SchemaDigest       string   `json:"schema_digest"`
	ComplianceDigest   string   `json:"compliance_digest"`
	License            string   `json:"license"`
	LicenseAttribution string   `json:"license_attribution"`
	Deviations         []string `json:"deviations"`
}

var deviations = []string{
	"Proxy-owned previous_response_id: continuation state is proxy-persisted canonical history, never forwarded raw to upstream backends",
	"Default base path /openresponses/v1 to prevent route collisions with /v1/responses",
	"No OpenAI-specific cancel route: OpenResponses omits POST /v1/responses/{id}/cancel",
	"WebSocket maximum connection age capped at 60 minutes with connection-local store:false state",
	"Context compaction standalone endpoint POST /responses/compact exposed as a protocol-neutral operation",
}

// GetProfileDescriptor returns the immutable official profile descriptor for OpenResponses 2026-04-24.
func GetProfileDescriptor() ProfileDescriptor {
	return ProfileDescriptor{
		Family:             ProtocolFamily,
		Version:            ProfileVersion,
		SourceCommit:       SourceCommit,
		SourceRepository:   SourceRepository,
		SchemaDigest:       ExpectedSchemaDigest,
		ComplianceDigest:   ExpectedComplianceDigest,
		License:            LicenseName,
		LicenseAttribution: LicenseAttribution,
		Deviations:         append([]string(nil), deviations...),
	}
}

// computeSHA256 returns the prefixed sha256: hex string for input bytes.
func computeSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%s", hex.EncodeToString(sum[:]))
}
