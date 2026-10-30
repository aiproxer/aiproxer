package providerprofiles

import (
	"fmt"
	"slices"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type Certification struct {
	ProfileID      string
	Family         Family
	FactoryKind    string
	EndpointURL    string
	AuthMode       AuthMode
	Headers        []SafeHeader
	ModelNamespace Namespace
	StaticModels   []Model
	Capabilities   []aipapi.Capability
	Dialects       aipapi.DialectSupport
	Quirks         []QuirkID
}

func Certify(p Profile) (Certification, error) {
	compiled, err := CompileProfile(p)
	if err != nil {
		return Certification{}, err
	}
	caps := make([]aipapi.Capability, 0, len(compiled.Capabilities))
	for capability := range compiled.Capabilities {
		caps = append(caps, capability)
	}
	slices.Sort(caps)

	return Certification{
		ProfileID:      p.ID,
		Family:         p.Family,
		FactoryKind:    compiled.Binding.FactoryKind,
		EndpointURL:    p.Endpoint.BaseURL,
		AuthMode:       p.Auth.Mode,
		Headers:        slices.Clone(p.Headers),
		ModelNamespace: p.Models.Namespace,
		StaticModels:   slices.Clone(p.Models.Static),
		Capabilities:   caps,
		Dialects:       compiled.Dialects,
		Quirks:         slices.Clone(p.Quirks),
	}, nil
}

func (c Certification) Validate() error {
	if c.ProfileID == "" || c.FactoryKind == "" || c.Family == "" || c.EndpointURL == "" || c.AuthMode == "" {
		return fmt.Errorf("incomplete profile certification")
	}
	return nil
}
