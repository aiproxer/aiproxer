// Ownership contracts for composed backend kind/prefix reservation.
//
// Two-stage validation: ValidateManifestOwnership (check-config / pre-activation)
// and ValidateResolvedOwnership (post-activation, before GenerationRuntime
// publication). Composition wires these through runtimebundle.
package pluginreg

import (
	"fmt"
	"strings"
)

// OriginKind identifies how a backend owner entered the composed catalog.
type OriginKind string

const (
	OriginBuiltIn           OriginKind = "built_in"
	OriginBuiltInCompatible OriginKind = "built_in_compatible"
	OriginExternalManifest  OriginKind = "external_manifest"
	OriginExternalResolved  OriginKind = "external_resolved"
)

// BackendOwner is an immutable bounded owner descriptor used in collision
// diagnostics. It never carries secrets or opaque YAML.
type BackendOwner struct {
	Origin      OriginKind
	FactoryKind string
	InstanceID  string
	Prefix      string
	SourceID    string
}

// OwnershipCollisionError names both owners of a conflicting kind or prefix.
type OwnershipCollisionError struct {
	Key    string
	Owners [2]BackendOwner
}

func (e *OwnershipCollisionError) Error() string {
	if e == nil {
		return "pluginreg: ownership collision"
	}
	a, b := e.Owners[0], e.Owners[1]
	return fmt.Sprintf(
		"pluginreg: ownership collision on %q between %s/%s (%s) and %s/%s (%s)",
		e.Key,
		a.Origin, a.FactoryKind, boundOwnerID(a),
		b.Origin, b.FactoryKind, boundOwnerID(b),
	)
}

func boundOwnerID(o BackendOwner) string {
	if o.InstanceID != "" {
		return o.InstanceID
	}
	if o.SourceID != "" {
		return o.SourceID
	}
	if o.Prefix != "" {
		return o.Prefix
	}
	return o.FactoryKind
}

// ManifestOwnershipInput is the check-config / pre-activation stage: built-ins,
// enabled generic instances, and manifest-discovered external factory kinds
// without launching plugin processes.
type ManifestOwnershipInput struct {
	BuiltIns       []BackendOwner
	GenericEnabled []BackendOwner
	ManifestKinds  []BackendOwner
}

// ResolvedOwnershipInput is the post-activation stage: advertised route
// prefixes from a (possibly fake) resolved profile, checked before generation
// publication.
type ResolvedOwnershipInput struct {
	Base             ManifestOwnershipInput
	ResolvedPrefixes []BackendOwner
}

// ValidatePrefixSyntax checks backend_prefix shape: non-empty after trim and
// containing neither '/' nor ':'.
func ValidatePrefixSyntax(prefix string) error {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return fmt.Errorf("pluginreg: backend_prefix is required")
	}
	if strings.Contains(prefix, "/") {
		return fmt.Errorf("pluginreg: backend_prefix %q must not contain '/'", prefix)
	}
	if strings.Contains(prefix, ":") {
		return fmt.Errorf("pluginreg: backend_prefix %q must not contain ':'", prefix)
	}
	return nil
}

// ValidateManifestOwnership validates the manifest-available ownership stage
// without process activation.
func ValidateManifestOwnership(in ManifestOwnershipInput) error {
	owners := make([]BackendOwner, 0, len(in.BuiltIns)+len(in.GenericEnabled)+len(in.ManifestKinds))
	owners = append(owners, in.BuiltIns...)
	owners = append(owners, in.GenericEnabled...)
	owners = append(owners, in.ManifestKinds...)
	return validateOwnerCollisions(owners)
}

// ValidateResolvedOwnership validates advertised external route prefixes
// against the composed catalog before generation publication.
func ValidateResolvedOwnership(in ResolvedOwnershipInput) error {
	owners := make([]BackendOwner, 0, len(in.Base.BuiltIns)+len(in.Base.GenericEnabled)+len(in.Base.ManifestKinds)+len(in.ResolvedPrefixes))
	owners = append(owners, in.Base.BuiltIns...)
	owners = append(owners, in.Base.GenericEnabled...)
	owners = append(owners, in.Base.ManifestKinds...)
	owners = append(owners, in.ResolvedPrefixes...)
	return validateOwnerCollisions(owners)
}

func validateOwnerCollisions(owners []BackendOwner) error {
	seen := make(map[string]BackendOwner, len(owners))
	for _, owner := range owners {
		keys := ownerKeys(owner)
		for _, key := range keys {
			if previous, ok := seen[key]; ok {
				return &OwnershipCollisionError{Key: key, Owners: [2]BackendOwner{previous, owner}}
			}
			seen[key] = owner
		}
	}
	return nil
}

func ownerKeys(owner BackendOwner) []string {
	keys := make([]string, 0, 2)
	if prefix := strings.TrimSpace(owner.Prefix); prefix != "" {
		keys = append(keys, prefix)
	}
	if owner.Origin == OriginExternalManifest {
		if kind := strings.TrimSpace(owner.FactoryKind); kind != "" && (len(keys) == 0 || keys[0] != kind) {
			keys = append(keys, kind)
		}
	}
	return keys
}
