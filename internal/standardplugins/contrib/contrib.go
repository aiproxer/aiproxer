// Package contrib contains immutable, metadata-only extension facets. It is a
// composition contract, not a runtime service locator.
package contrib

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

type RegistrationSource string

const (
	SourceBuiltin           RegistrationSource = "builtin"
	SourceBuiltinCompatible RegistrationSource = "built_in_compatible"
)

type RegistrationFacet struct {
	ID     string
	Source RegistrationSource
}

type FrontendRegistrationFacet struct {
	ID string
}

type BackendRegistrationFacet struct {
	ID              string
	Source          RegistrationSource
	EssentialOrder  int
	CompatibleOrder int
}

// RouteClaimFacet is the cycle-neutral shape of a route ownership claim.
// Runtime route providers may add config-derived claims at composition time.
type RouteClaimFacet struct {
	Method      string
	Path        string
	OperationID string
}

// RouteFacet declares that the contribution owns route claims. The executable
// provider is held by the standard-distribution composition layer, not this
// cycle-neutral metadata package.
type RouteFacet struct {
	Declared bool
	Claims   []RouteClaimFacet
}

// DiagnosticFacet declares that the contribution owns an operator projector.
// ID identifies the metadata owner; the executable projector stays at the
// runtime edge and is not a service bag.
type DiagnosticFacet struct {
	ID       string
	Declared bool
}
type ContractSubject struct {
	ID   string
	Kind string
}
type (
	ContractFacet         struct{ Subject ContractSubject }
	CompatibleFamilyFacet struct {
		FamilyID   string
		ProfileIDs []string
	}
)

type FrontendContribution struct {
	Registration FrontendRegistrationFacet
	Routes       *RouteFacet
	Diagnostics  *DiagnosticFacet
	Contract     ContractFacet
}

type BackendContribution struct {
	Registration BackendRegistrationFacet
	Diagnostics  *DiagnosticFacet
	Contract     ContractFacet
	Compatible   *CompatibleFamilyFacet
}

type ContributionSet struct {
	Frontends []FrontendContribution
	Backends  []BackendContribution
	// Diagnostics contains catalog/composition-owned projectors that do not
	// correspond to a single frontend or backend registration. Keeping this
	// facet in the derived source-of-truth prevents hidden projector lists.
	Diagnostics []DiagnosticFacet
}

type Views struct {
	Frontends        []FrontendContribution
	Backends         []BackendContribution
	Routes           []RouteFacet
	RouteClaims      []RouteClaimFacet
	Diagnostics      []DiagnosticFacet
	ContractSubjects []ContractSubject
	EssentialIDs     []string
	FamilyIDs        []string
	CompatibleIDs    []string
	ProfileFamilies  map[string]string
}

func Derive(in ContributionSet) (Views, error) {
	views := Views{ProfileFamilies: map[string]string{}}
	seenFrontends := map[string]struct{}{}
	seenBackends := map[string]struct{}{}
	seenContracts := map[string]struct{}{}
	seenRoutes := map[string]struct{}{}
	seenDiagnostics := map[string]struct{}{}
	for _, f := range in.Frontends {
		if err := addScopedID(seenFrontends, "frontend", f.Registration.ID); err != nil {
			return Views{}, err
		}
		views.Frontends = append(views.Frontends, f)
		if f.Routes != nil && f.Routes.Declared {
			views.Routes = append(views.Routes, *f.Routes)
			for _, claim := range f.Routes.Claims {
				normalized, err := normalizeRouteClaim(claim)
				if err != nil {
					return Views{}, fmt.Errorf("frontend %q: %w", f.Registration.ID, err)
				}
				key := normalized.Method + "\x00" + normalized.Path
				if _, exists := seenRoutes[key]; exists {
					return Views{}, fmt.Errorf("duplicate route claim: %s %s", normalized.Method, normalized.Path)
				}
				seenRoutes[key] = struct{}{}
				views.RouteClaims = append(views.RouteClaims, normalized)
			}
		}
		if f.Diagnostics != nil && f.Diagnostics.Declared {
			if err := appendDiagnosticFacet(&views, seenDiagnostics, *f.Diagnostics, "frontend", f.Registration.ID); err != nil {
				return Views{}, err
			}
		}
		if f.Contract.Subject.ID != "" {
			contractKey := f.Contract.Subject.Kind + "\x00" + f.Contract.Subject.ID
			if _, exists := seenContracts[contractKey]; exists {
				return Views{}, fmt.Errorf("duplicate contract subject: %s", f.Contract.Subject.ID)
			}
			seenContracts[contractKey] = struct{}{}
			views.ContractSubjects = append(views.ContractSubjects, f.Contract.Subject)
		}
	}
	for _, d := range in.Diagnostics {
		if !d.Declared {
			continue
		}
		if err := appendDiagnosticFacet(&views, seenDiagnostics, d, "composition", ""); err != nil {
			return Views{}, err
		}
	}
	for _, b := range in.Backends {
		if err := addScopedID(seenBackends, "backend", b.Registration.ID); err != nil {
			return Views{}, err
		}
		views.Backends = append(views.Backends, b)
		source := b.Registration.Source
		if source == "" {
			source = SourceBuiltin
		}
		if source == SourceBuiltin || source == SourceBuiltinCompatible {
			views.EssentialIDs = append(views.EssentialIDs, b.Registration.ID)
		}
		if b.Diagnostics != nil && b.Diagnostics.Declared {
			if err := appendDiagnosticFacet(&views, seenDiagnostics, *b.Diagnostics, "backend", b.Registration.ID); err != nil {
				return Views{}, err
			}
		}
		if b.Contract.Subject.ID != "" {
			contractKey := b.Contract.Subject.Kind + "\x00" + b.Contract.Subject.ID
			if _, exists := seenContracts[contractKey]; exists {
				return Views{}, fmt.Errorf("duplicate contract subject: %s", b.Contract.Subject.ID)
			}
			seenContracts[contractKey] = struct{}{}
			views.ContractSubjects = append(views.ContractSubjects, b.Contract.Subject)
		}
		if b.Compatible != nil && (source == SourceBuiltinCompatible || source == SourceBuiltin) {
			views.CompatibleIDs = append(views.CompatibleIDs, b.Registration.ID)
			family := strings.TrimSpace(b.Compatible.FamilyID)
			if family == "" {
				return Views{}, fmt.Errorf("contribution %q: empty compatible family", b.Registration.ID)
			}
			if slices.Contains(views.FamilyIDs, family) {
				return Views{}, fmt.Errorf("duplicate compatible family: %s", family)
			}
			views.FamilyIDs = append(views.FamilyIDs, family)
			for _, profile := range b.Compatible.ProfileIDs {
				profile = strings.TrimSpace(profile)
				if profile == "" {
					return Views{}, fmt.Errorf("contribution %q: empty profile id", b.Registration.ID)
				}
				if old, ok := views.ProfileFamilies[profile]; ok {
					return Views{}, fmt.Errorf("duplicate provider profile %q bound to %q and %q", profile, old, family)
				}
				views.ProfileFamilies[profile] = family
			}
		}
	}
	// Contribution declaration order is the stable distribution order. Keep all
	// projections in that order so registration and operator output do not drift.
	// Compatible factory IDs retain their established deterministic lexical view.
	// Compatible IDs have historically been exposed in lexical order.
	if len(views.CompatibleIDs) > 1 {
		sort.SliceStable(views.CompatibleIDs, func(i, j int) bool {
			return compatibleRank(in.Backends, views.CompatibleIDs[i]) < compatibleRank(in.Backends, views.CompatibleIDs[j])
		})
	}
	if len(views.EssentialIDs) > 1 {
		sort.SliceStable(views.EssentialIDs, func(i, j int) bool {
			return essentialRank(in.Backends, views.EssentialIDs[i]) < essentialRank(in.Backends, views.EssentialIDs[j])
		})
	}
	return views, nil
}

func appendDiagnosticFacet(views *Views, seen map[string]struct{}, facet DiagnosticFacet, scope, owner string) error {
	id := strings.TrimSpace(facet.ID)
	if id == "" && owner != "" {
		id = owner + ":diagnostics"
	}
	if id == "" {
		return fmt.Errorf("diagnostic contribution: empty id")
	}
	if _, exists := seen[id]; exists {
		return fmt.Errorf("duplicate diagnostic contribution id %q", id)
	}
	seen[id] = struct{}{}
	facet.ID = id
	views.Diagnostics = append(views.Diagnostics, facet)
	return nil
}

func normalizeRouteClaim(in RouteClaimFacet) (RouteClaimFacet, error) {
	method := strings.ToUpper(strings.TrimSpace(in.Method))
	path := strings.TrimSpace(in.Path)
	operation := strings.TrimSpace(in.OperationID)
	if method == "" || path == "" || operation == "" {
		return RouteClaimFacet{}, fmt.Errorf("route claim requires method, path, and operation id")
	}
	if !strings.HasPrefix(path, "/") || strings.Contains(path, "//") || strings.ContainsAny(path, "?#*\\") {
		return RouteClaimFacet{}, fmt.Errorf("invalid route claim path %q", in.Path)
	}
	if len(operation) > 96 || strings.ContainsAny(operation, "\r\n\t") {
		return RouteClaimFacet{}, fmt.Errorf("invalid route claim operation id")
	}
	return RouteClaimFacet{Method: method, Path: strings.TrimRight(path, "/"), OperationID: operation}, nil
}

func compatibleRank(backends []BackendContribution, id string) int {
	for _, b := range backends {
		if b.Registration.ID == id {
			return b.Registration.CompatibleOrder
		}
	}
	return 0
}

func essentialRank(backends []BackendContribution, id string) int {
	for _, b := range backends {
		if b.Registration.ID == id {
			return b.Registration.EssentialOrder
		}
	}
	return 0
}

func addScopedID(seen map[string]struct{}, kind, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("contribution: empty %s id", kind)
	}
	if _, ok := seen[id]; ok {
		return fmt.Errorf("duplicate %s contribution id %q", kind, id)
	}
	seen[id] = struct{}{}
	return nil
}

func (v Views) FrontendIDs() []string {
	out := make([]string, 0, len(v.Frontends))
	for _, f := range v.Frontends {
		out = append(out, f.Registration.ID)
	}
	return out
}

func (v Views) BackendIDs() []string {
	out := make([]string, 0, len(v.Backends))
	for _, b := range v.Backends {
		out = append(out, b.Registration.ID)
	}
	return out
}
