package pluginreg

import (
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func registryFactoryKey(r aipsdk.Requirement) string {
	if s := strings.TrimSpace(r.RegistryFactoryID); s != "" {
		return s
	}
	return r.ID
}

// ValidateBundledFactories ensures every mandatory plugin has a registered factory for its kind on r.
func (r *Registry) ValidateBundledFactories(required []aipsdk.Requirement) error {
	for _, req := range required {
		key := registryFactoryKey(req)
		switch req.Kind {
		case aipsdk.PluginKindBackend:
			if _, err := r.lookupBackend(key); err != nil {
				return err
			}
		case aipsdk.PluginKindFrontend:
			if _, err := r.lookupFrontend(key); err != nil {
				return err
			}
		case aipsdk.PluginKindFeature:
			if _, err := r.lookupFeature(key); err != nil {
				return err
			}
		default:
			return fmt.Errorf("pluginreg: unknown plugin kind %q for id %q", req.Kind, req.ID)
		}
	}
	return nil
}

func (r *Registry) lookupBackend(id string) (BackendFactory, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	fn, ok := r.backends[id]
	if !ok {
		return nil, fmt.Errorf("pluginreg: mandatory backend %q has no registered factory", id)
	}
	return fn, nil
}

func (r *Registry) lookupFrontend(id string) (FrontendMount, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	fn, ok := r.frontends[id]
	if !ok {
		return nil, fmt.Errorf("pluginreg: mandatory frontend %q has no registered factory", id)
	}
	return fn, nil
}

func (r *Registry) lookupFeature(id string) (FeatureFactory, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	fn, ok := r.features[id]
	if !ok {
		return nil, fmt.Errorf("pluginreg: mandatory feature %q has no registered factory", id)
	}
	return fn, nil
}
