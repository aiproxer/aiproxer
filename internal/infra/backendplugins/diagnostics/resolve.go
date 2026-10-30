package diagnostics

import (
	"errors"
	"fmt"

	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/catalog"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/discovery"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
)

// CatalogResolution is the immutable discover→trust→catalog snapshot shared by
// inspect reporting and serve install preparation.
type CatalogResolution struct {
	Discovered  []discovery.Descriptor
	TrustBySafe map[string]trust.VerifyResult
	Snapshot    catalog.Snapshot
	CatalogErr  error
	HostMajor   uint32
	HostMinor   uint32
}

// ResolveCatalog runs deterministic discovery, trust verification, and catalog
// resolve without launching processes. Inspect and serve must call this (or a
// thin wrapper) so conflict/reason policy cannot diverge.
func ResolveCatalog(in InspectInput) (CatalogResolution, error) {
	hostMajor := in.HostMajor
	if hostMajor == 0 {
		hostMajor = DefaultHostProtocolMajor
	}
	hostMinor := in.HostMinor
	discoverFn := in.Discover
	if discoverFn == nil {
		discoverFn = discovery.Discover
	}
	trustFn := in.Trust
	if trustFn == nil {
		trustFn = trust.Verify
	}

	var discovered []discovery.Descriptor
	if in.DiscoveryEnabled {
		res, err := discoverFn(in.Discovery)
		if err != nil {
			if !errors.Is(err, discovery.ErrNoRoots) {
				return CatalogResolution{}, fmt.Errorf("diagnostics/resolve: discover: %w", err)
			}
		} else {
			discovered = res.Descriptors
		}
	}

	trustBySafe := map[string]trust.VerifyResult{}
	for _, d := range discovered {
		if d.Status != discovery.StatusDiscovered {
			continue
		}
		trustBySafe[d.SafeID] = trustFn(d.Root, d.Manifest, trust.VerifyOptions{StagingDir: in.StagingDir})
	}

	builtinSet := map[string]struct{}{}
	for _, k := range in.BuiltinKinds {
		builtinSet[k] = struct{}{}
	}

	enabledKinds := make([]string, 0, len(in.Configured))
	for _, c := range in.Configured {
		if !c.Enabled || c.Kind == "" {
			continue
		}
		if _, isBuiltin := builtinSet[c.Kind]; isBuiltin {
			continue
		}
		enabledKinds = append(enabledKinds, c.Kind)
	}

	snap, catErr := catalog.Resolve(catalog.Input{
		Discovered:   discovered,
		TrustBySafe:  trustBySafe,
		BuiltinKinds: append([]string(nil), in.BuiltinKinds...),
		EnabledKinds: enabledKinds,
		Strict:       in.Strict,
		HostMajor:    hostMajor,
		HostMinor:    hostMinor,
	})

	return CatalogResolution{
		Discovered:  discovered,
		TrustBySafe: trustBySafe,
		Snapshot:    snap,
		CatalogErr:  catErr,
		HostMajor:   hostMajor,
		HostMinor:   hostMinor,
	}, nil
}
