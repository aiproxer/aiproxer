package runtimebundle

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"hash"
	"sort"
	"strconv"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

var errBackendResourceIdentityIncomplete = errors.New("runtimebundle: incomplete backend resource physical identity")

// backendResourcePhysicalInput is the complete effective input at the
// discovered connector construction/configure boundary. Keep this DTO
// package-private and update the explicit identity projection when a new
// configure-time or launch-time input is added.
//
// ArtifactDigest, ProcessModel, and RuntimePolicy are currently startup-fixed
// for discovered factories. They remain in this physical identity so that a
// future lifetime change cannot silently make distinct resources reusable.
type backendResourcePhysicalInput struct {
	InstanceID     string
	FactoryKind    string
	ArtifactDigest string
	ProcessModel   processhost.ProcessModel
	ConfigureYAML  []byte
	RuntimePolicy  backendplugin.RuntimePolicy
	Secrets        backendplugin.SecretBundle
	LaunchEnv      map[string]string
	// CredentialEnvRoot is the nonsecret approved provider namespace the
	// secrets were captured from (empty for non-inference inputs).
	CredentialEnvRoot string
	// CredentialQuotaDomain is the nonsecret provider/deployment quota scope
	// (empty for non-inference inputs).
	CredentialQuotaDomain string
	// CredentialSlot is the canonical secret-slot base name (api_key or
	// api_token; empty for non-inference inputs).
	CredentialSlot string
	// CredentialSelection is the contract selection mode
	// (quota_pre_output, session_admission or no_rotation).
	CredentialSelection string
	// CredentialPolicyRevision is the nonsecret upstream-credential policy
	// revision effective at capture (scan bound and quota-switch threshold).
	CredentialPolicyRevision string
	// ConnectorEvidencePolicy is the host policy for connector economic
	// evidence the host cannot admit. It is startup-fixed, like the runtime
	// policy, and therefore part of the physical identity.
	ConnectorEvidencePolicy execbackend.ConnectorEconomicEvidencePolicy
}

// backendResourceIdentity is an opaque, comparable physical-resource key.
// It contains only a domain-separated SHA-256 digest; in particular, it does
// not retain configuration bytes or configure-time secret material.
type backendResourceIdentity struct {
	digest [sha256.Size]byte
}

// physicalIdentity derives the private physical connector identity. The
// caller must use the isolated construction path when shareable is false.
// Missing required identity inputs fail closed; known non-pooled process
// models deliberately return a non-shareable result without an error so that
// they can continue through the existing generation-local path.
func physicalIdentity(input backendResourcePhysicalInput) (backendResourceIdentity, bool, error) {
	if strings.TrimSpace(input.InstanceID) == "" ||
		strings.TrimSpace(input.FactoryKind) == "" ||
		strings.TrimSpace(input.ArtifactDigest) == "" ||
		strings.TrimSpace(string(input.ProcessModel)) == "" {
		return backendResourceIdentity{}, false, errBackendResourceIdentityIncomplete
	}

	switch input.ProcessModel {
	case processhost.ProcessModelPerInstance:
		// Eligible for cross-generation physical reuse.
	case processhost.ProcessModelSharedArtifact:
		// Shared-artifact ownership has different lifecycle and concurrency
		// semantics; it remains on the established isolated path.
		return backendResourceIdentity{}, false, nil
	default:
		// Unknown models must never opt into pooling by accident.
		return backendResourceIdentity{}, false, nil
	}

	var identity framedSHA256
	identity.frameString("aip/backend-resource-physical-identity/v1")
	identity.frameString("instance_id")
	identity.frameString(input.InstanceID)
	identity.frameString("factory_kind")
	identity.frameString(input.FactoryKind)
	identity.frameString("artifact_digest")
	identity.frameString(input.ArtifactDigest)
	identity.frameString("process_model")
	identity.frameString(string(input.ProcessModel))
	identity.frameString("configure_yaml")
	identity.frameBytes(input.ConfigureYAML)

	identity.frameString("runtime_policy")
	writeRuntimePolicy(&identity, input.RuntimePolicy)

	// The connector economic evidence policy changes the adapter's observable
	// behaviour, so it is part of the physical identity: two runtimes with
	// different policies must never share one pooled connector resource.
	identity.frameString("connector_evidence_policy")
	identity.frameString(string(input.ConnectorEvidencePolicy))

	identity.frameString("secrets")
	secretFingerprint := fingerprintBackendResourceSecrets(input.Secrets)
	identity.frameBytes(secretFingerprint[:])
	identity.frameString("execution_context")
	names := make([]string, 0, len(input.LaunchEnv))
	for name := range input.LaunchEnv {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		identity.frameString(name)
		identity.frameString(input.LaunchEnv[name])
	}

	// Credential namespace binding (design step 5): the approved provider
	// namespace, quota domain, slot shape, selection mode and capture-time
	// policy revision are nonsecret effective inputs. Framing them keeps
	// distinct credential snapshots from sharing one pooled resource; empty
	// values keep non-inference inputs on a stable distinct identity.
	identity.frameString("credential_namespace")
	identity.frameString(input.CredentialEnvRoot)
	identity.frameString("quota_domain")
	identity.frameString(input.CredentialQuotaDomain)
	identity.frameString("credential_slot")
	identity.frameString(input.CredentialSlot)
	identity.frameString("credential_selection")
	identity.frameString(input.CredentialSelection)
	identity.frameString("credential_policy_revision")
	identity.frameString(input.CredentialPolicyRevision)

	var digest [sha256.Size]byte
	copy(digest[:], identity.sum())
	return backendResourceIdentity{digest: digest}, true, nil
}

// writeRuntimePolicy is intentionally an explicit projection. Do not replace
// this with reflection or serialization: every field is part of the effective
// configure-time identity and a newly added field requires deliberate review.
func writeRuntimePolicy(dst *framedSHA256, policy backendplugin.RuntimePolicy) {
	dst.frameString("max_request_bytes")
	dst.frameUint64(policy.MaxRequestBytes)
	dst.frameString("max_stream_frame_bytes")
	dst.frameUint64(policy.MaxStreamFrameBytes)
	dst.frameString("max_pending_events")
	dst.frameUint64(policy.MaxPendingEvents)
	dst.frameString("request_timeout_ms")
	dst.frameInt64(policy.RequestTimeoutMS)
	dst.frameString("cancel_deadline_ms")
	dst.frameInt64(policy.CancelDeadlineMS)
	dst.frameString("diagnostics_verbosity")
	dst.frameString(policy.DiagnosticsVerbosity)
	dst.frameString("max_concurrent_executions")
	dst.frameUint32(policy.MaxConcurrentExecutions)
	dst.frameString("local_only")
	dst.frameBool(policy.LocalOnly)
	dst.frameString("allowed_env_names")
	for _, name := range normalizedAllowedEnvNames(policy.AllowedEnvNames) {
		dst.frameString(name)
	}
	dst.frameString("disable_transport_retries")
	// This value is projected explicitly rather than inherited from a
	// serialized struct. processhost makes this host-owned setting effective
	// before Configure, so identity construction treats it as normalized.
	dst.frameBool(true)
	dst.frameString("quota_switch_retry_after_ms")
	dst.frameInt64(policy.QuotaSwitchRetryAfterMS)
}

func normalizedAllowedEnvNames(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	normalized := append([]string(nil), names...)
	sort.Strings(normalized)
	unique := normalized[:0]
	for _, name := range normalized {
		if len(unique) == 0 || unique[len(unique)-1] != name {
			unique = append(unique, name)
		}
	}
	return unique
}

// fingerprintBackendResourceSecrets hashes sorted, length-framed secret
// names and values into a private digest. Only the digest is placed in the
// physical identity; the returned value never contains plaintext material.
func fingerprintBackendResourceSecrets(secrets backendplugin.SecretBundle) [sha256.Size]byte {
	var fingerprint framedSHA256
	fingerprint.frameString("aip/backend-resource-secret-fingerprint/v1")
	if len(secrets.Values) == 0 {
		return fingerprint.sumArray()
	}

	for _, name := range orderedCredentialSecretNames(secrets.Values) {
		fingerprint.frameString(name)
		fingerprint.frameBytes(secrets.Values[name])
	}
	return fingerprint.sumArray()
}

// orderedCredentialSecretNames returns secret names in normalized
// credential-slot order: names sharing a slot base sort with the bare base
// first and numbered suffixes in ascending numeric order (api_key,
// api_key_2, api_key_10), so ordered Configure slots bind stably regardless
// of map iteration order. Names without a numeric suffix keep byte order
// among themselves.
func orderedCredentialSecretNames(values map[string][]byte) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return credentialSlotLess(names[i], names[j]) })
	return names
}

// credentialSlotLess orders slot names by base, then bare-before-numbered,
// then numeric suffix, then full name. A trailing _N with N>=2 is a numbered
// slot; anything else (including _0/_1, which the resolver never emits)
// compares as a plain base name.
func credentialSlotLess(a, b string) bool {
	pa, na, oka := splitCredentialSlot(a)
	pb, nb, okb := splitCredentialSlot(b)
	if pa != pb {
		return pa < pb
	}
	if oka != okb {
		return !oka
	}
	if oka && na != nb {
		return na < nb
	}
	return a < b
}

func splitCredentialSlot(name string) (base string, index int, numbered bool) {
	idx := strings.LastIndexByte(name, '_')
	if idx < 0 {
		return name, 0, false
	}
	n, err := strconv.Atoi(name[idx+1:])
	if err != nil || n < 2 {
		return name, 0, false
	}
	return name[:idx], n, true
}

type framedSHA256 struct {
	hash hash.Hash
}

func (f *framedSHA256) ensureHash() {
	if f.hash == nil {
		f.hash = sha256.New()
	}
}

func (f *framedSHA256) frameBytes(value []byte) {
	f.ensureHash()
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = f.hash.Write(length[:])
	_, _ = f.hash.Write(value)
}

func (f *framedSHA256) frameString(value string) {
	f.frameBytes([]byte(value))
}

func (f *framedSHA256) frameUint64(value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	f.frameBytes(encoded[:])
}

func (f *framedSHA256) frameInt64(value int64) {
	f.frameUint64(uint64(value))
}

func (f *framedSHA256) frameUint32(value uint32) {
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], value)
	f.frameBytes(encoded[:])
}

func (f *framedSHA256) frameBool(value bool) {
	if value {
		f.frameBytes([]byte{1})
		return
	}
	f.frameBytes([]byte{0})
}

func (f *framedSHA256) sum() []byte {
	f.ensureHash()
	return f.hash.Sum(nil)
}

func (f *framedSHA256) sumArray() [sha256.Size]byte {
	var result [sha256.Size]byte
	copy(result[:], f.sum())
	return result
}
