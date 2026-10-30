package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"maps"
	"slices"
	"strings"

	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// LocalAPIKeyAuthenticator validates bearer API keys against operator-configured records.
// Records must pass [ValidateLocalAPIKeyRecords] before construction.
type LocalAPIKeyAuthenticator struct {
	records []localAPIKeyEntry
}

type localAPIKeyEntry struct {
	keyID        string
	principalID  string
	secretDigest [sha256.Size]byte
	fingerprint  string
	attribution  LocalAttribution
}

// NewLocalAPIKeyAuthenticator builds an authenticator from validated key records.
func NewLocalAPIKeyAuthenticator(records []LocalAPIKeyRecord) (*LocalAPIKeyAuthenticator, error) {
	if err := ValidateLocalAPIKeyRecords(records); err != nil {
		return nil, err
	}
	out := make([]localAPIKeyEntry, 0, len(records))
	for _, r := range records {
		kid := strings.TrimSpace(r.KeyID)
		pid := strings.TrimSpace(r.PrincipalID)
		sec := strings.TrimSpace(r.Key)
		out = append(out, localAPIKeyEntry{
			keyID:        kid,
			principalID:  pid,
			secretDigest: sha256.Sum256([]byte(sec)),
			fingerprint:  redactedAPIKeyFingerprint(kid, sec),
			attribution:  r.Attribution,
		})
	}
	return &LocalAPIKeyAuthenticator{records: out}, nil
}

// redactedAPIKeyFingerprint derives a stable, non-reversible-at-a-glance fingerprint using
// HMAC-SHA256 keyed by non-secret key_id over the secret (design: fingerprinting, not storage).
func redactedAPIKeyFingerprint(keyID, secret string) string {
	mac := hmac.New(sha256.New, []byte(keyID))
	_, _ = mac.Write([]byte(secret))
	sum := mac.Sum(nil)
	return hex.EncodeToString(sum[:12])
}

// Authenticate implements [Authenticator].
func (a *LocalAPIKeyAuthenticator) Authenticate(ctx context.Context, req sdkauth.InboundCallMeta) (sdkauth.Decision, error) {
	_ = ctx
	if a == nil || len(a.records) == 0 {
		return sdkauth.Decision{Outcome: sdkauth.OutcomeDeny, ReasonCode: "api_key_misconfigured"}, nil
	}
	raw := stripBearer(strings.TrimSpace(req.AuthorizationBearer))
	if len(raw) == 0 {
		return sdkauth.Decision{Outcome: sdkauth.OutcomeDeny, ReasonCode: "missing_api_key"}, nil
	}
	presentedDigest := sha256.Sum256([]byte(raw))
	matched := -1
	for i, r := range a.records {
		if subtle.ConstantTimeCompare(presentedDigest[:], r.secretDigest[:]) == 1 {
			matched = i
		}
	}
	if matched >= 0 {
		r := a.records[matched]
		principal := execview.PrincipalView{ID: strings.TrimSpace(r.principalID)}
		return sdkauth.Decision{
			Outcome:        sdkauth.OutcomeAllow,
			Principal:      principal,
			Device:         sdkauth.DeviceIdentity{ID: r.principalID + ":" + r.keyID, KeyID: r.keyID, Fingerprint: r.fingerprint},
			SatisfiedLevel: sdkauth.LevelAPIKey,
			Scope:          scopeFromLocalAPIKey(r),
		}, nil
	}
	return sdkauth.Decision{Outcome: sdkauth.OutcomeDeny, ReasonCode: "unknown_api_key"}, nil
}

// scopeFromLocalAPIKey builds the authoritative scope from a matched record's non-secret
// attribution. AuthMethod defaults to "local_api_key" (the authenticator knows its method).
// Missing optional fields remain unknown (requirement 3.5). Raw key material never enters.
func scopeFromLocalAPIKey(r localAPIKeyEntry) *scope.PrincipalScopeView {
	return &scope.PrincipalScopeView{
		SubjectKind:    scope.SubjectService,
		Origin:         scope.OriginClient,
		PrincipalID:    scope.Known(r.principalID),
		CredentialID:   scope.Known(r.keyID),
		AuthMethod:     knownOrDefault(r.attribution.AuthMethod, "local_api_key"),
		DisplayName:    knownOrUnknown(r.attribution.DisplayName),
		TenantID:       knownOrUnknown(r.attribution.TenantID),
		OrganizationID: knownOrUnknown(r.attribution.OrganizationID),
		WorkspaceID:    knownOrUnknown(r.attribution.WorkspaceID),
		ProjectID:      knownOrUnknown(r.attribution.ProjectID),
		DepartmentID:   knownOrUnknown(r.attribution.DepartmentID),
		CostCenterID:   knownOrUnknown(r.attribution.CostCenterID),
		Roles:          slices.Clone(r.attribution.Roles),
		SafeClaims:     maps.Clone(r.attribution.SafeClaims),
		PolicyLabels:   maps.Clone(r.attribution.PolicyLabels),
	}
}

func knownOrUnknown(configured string) scope.Value {
	if v := strings.TrimSpace(configured); v != "" {
		return scope.Known(v)
	}
	return scope.Unknown()
}

func knownOrDefault(configured, def string) scope.Value {
	if v := strings.TrimSpace(configured); v != "" {
		return scope.Known(v)
	}
	return scope.Known(def)
}

func stripBearer(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 7 && strings.EqualFold(s[:7], "bearer ") {
		return strings.TrimSpace(s[7:])
	}
	return s
}
