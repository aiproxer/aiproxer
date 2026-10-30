package runtimebundle

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"gopkg.in/yaml.v3"
)

// BackendStateIdentity is the canonical key for affinity/health observation reuse
// across request-plane candidates (requirements 6.7–6.8).
type BackendStateIdentity struct {
	InstanceID   string
	FactoryKind  string
	ConfigDigest string
}

// Compatible reports whether two identities may safely share affinity/health state.
func (k BackendStateIdentity) Compatible(other BackendStateIdentity) bool {
	return k.InstanceID == other.InstanceID &&
		k.FactoryKind == other.FactoryKind &&
		k.ConfigDigest == other.ConfigDigest &&
		k.InstanceID != ""
}

// Namespace returns a stable opaque prefix for keyed observation storage.
func (k BackendStateIdentity) Namespace() string {
	if k.InstanceID == "" {
		return ""
	}
	return k.FactoryKind + "/" + k.InstanceID + "@" + k.ConfigDigest
}

// BackendStateIdentityFromPlugin derives a process state identity from one backend row.
func BackendStateIdentityFromPlugin(p config.PluginConfig) (BackendStateIdentity, error) {
	id := p.InstanceID()
	kind := p.FactoryID()
	if id == "" {
		return BackendStateIdentity{}, fmt.Errorf("runtimebundle: empty backend instance id")
	}
	configRaw, err := marshalPluginConfigNode(p.Config)
	if err != nil {
		return BackendStateIdentity{}, fmt.Errorf("runtimebundle: backend identity marshal: %w", err)
	}
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "kind=%s\nid=%s\nenabled=%v\n", kind, id, p.Enabled)
	_, _ = h.Write(configRaw)
	sum := h.Sum(nil)
	return BackendStateIdentity{
		InstanceID:   id,
		FactoryKind:  kind,
		ConfigDigest: hex.EncodeToString(sum[:16]),
	}, nil
}

func marshalPluginConfigNode(n yaml.Node) ([]byte, error) {
	if n.Kind == 0 && n.Tag == "" && n.Value == "" && len(n.Content) == 0 {
		return nil, nil
	}
	node := n
	if node.Kind == yaml.DocumentNode {
		if len(node.Content) == 0 {
			return nil, nil
		}
		node = *node.Content[0]
	}
	return yaml.Marshal(&node)
}

// BackendStateIdentitiesFromConfig maps instance id → identity for enabled and
// disabled backend rows present in cfg (absent ids are simply omitted).
func BackendStateIdentitiesFromConfig(cfg *config.Config) (map[string]BackendStateIdentity, error) {
	out := make(map[string]BackendStateIdentity)
	if cfg == nil {
		return out, nil
	}
	for _, p := range cfg.Plugins.Backends {
		id := p.InstanceID()
		if id == "" {
			continue
		}
		key, err := BackendStateIdentityFromPlugin(p)
		if err != nil {
			return nil, err
		}
		out[id] = key
	}
	return out, nil
}
