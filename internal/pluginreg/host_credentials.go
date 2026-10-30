package pluginreg

import "time"

// HostCredentialSnapshot is the authorized per-candidate capture of numbered
// static-credential slots, keyed by bare environment root. The composition
// root resolves it once per candidate (startup or reload) through the
// policy-driven host resolver and hands it to in-process backend factories via
// GenericBackendFactoryDeps, so serving paths use admitted values instead of
// reading the process environment per request.
//
// A zero value (nil ByRoot) means no snapshot was captured for this build:
// factories fall back to their legacy source (Install-time closure or
// candidate-build environment lookup). A non-nil ByRoot is authoritative: a
// missing root resolves to no keys rather than falling back.
//
// All values are secret material: never log them or include them in errors.
type HostCredentialSnapshot struct {
	// ByRoot maps a bare environment root (e.g. OPENAI_API_KEY) to its
	// ordered slot values (slot 1 is the bare root).
	ByRoot map[string][]string
	// MaxEnvIndex is the nonsecret scan bound the snapshot was captured with.
	MaxEnvIndex int
	// QuotaSwitchRetryAfter is the nonsecret proxy-wide quota-switch
	// threshold bound at capture from upstream_credentials. Zero selects the
	// coordinator default.
	QuotaSwitchRetryAfter time.Duration
}

// Resolved reports whether the snapshot is an authoritative capture.
func (s HostCredentialSnapshot) Resolved() bool {
	return s.ByRoot != nil
}

// Keys returns a defensive copy of the ordered slot values for root, or nil
// when the root captured no keys.
func (s HostCredentialSnapshot) Keys(root string) []string {
	if s.ByRoot == nil {
		return nil
	}
	return append([]string(nil), s.ByRoot[root]...)
}
