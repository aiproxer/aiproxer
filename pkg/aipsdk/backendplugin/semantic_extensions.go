package backendplugin

import (
	"encoding/json"
	"fmt"
	"strings"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func semanticExtensionFromProto(in *backendpluginv1.SemanticExtensionWire) (SemanticExtension, error) {
	if in == nil {
		return SemanticExtension{}, ErrInvalidInvocation
	}
	out := SemanticExtension{Namespace: in.GetNamespace(), Type: in.GetType(), Implementor: in.GetImplementor(), Direction: in.GetDirection(), Data: RawJSONAbsentValue()}
	switch in.GetPresence() {
	case backendpluginv1.SemanticExtensionWire_PRESENCE_NULL:
		out.Presence = SemanticExtensionNull
	case backendpluginv1.SemanticExtensionWire_PRESENCE_VALUE:
		out.Presence = SemanticExtensionValue
		out.Data = RawJSONFromBytes(in.GetJson())
	default:
		return SemanticExtension{}, fmt.Errorf("%w: semantic extension presence", ErrInvalidInvocation)
	}
	return out, validateSemanticExtension(out)
}

func semanticExtensionToProto(in SemanticExtension) (*backendpluginv1.SemanticExtensionWire, error) {
	if err := validateSemanticExtension(in); err != nil {
		return nil, err
	}
	out := &backendpluginv1.SemanticExtensionWire{Namespace: in.Namespace, Type: in.Type, Implementor: in.Implementor, Direction: in.Direction}
	switch in.Presence {
	case SemanticExtensionNull:
		out.Presence = backendpluginv1.SemanticExtensionWire_PRESENCE_NULL
	case SemanticExtensionValue:
		out.Presence = backendpluginv1.SemanticExtensionWire_PRESENCE_VALUE
		out.Json = append([]byte(nil), in.Data.Bytes()...)
	}
	return out, nil
}

func validateSemanticExtension(ext SemanticExtension) error {
	if ext.Namespace == "" || ext.Type == "" || ext.Implementor == "" || ext.Direction == "" || len(ext.Namespace) > aipapi.MaxExtensionNamespaceBytes || len(ext.Type) > aipapi.MaxExtensionTypeBytes || len(ext.Implementor) > aipapi.MaxExtensionImplementorBytes || len(ext.Direction) > aipapi.MaxExtensionDirectionBytes {
		return fmt.Errorf("%w: semantic extension identity", ErrInvalidInvocation)
	}
	for _, value := range []string{ext.Namespace, ext.Type, ext.Implementor} {
		if value != strings.ToLower(value) || strings.ContainsAny(value, " \t\r\n/\\:") {
			return fmt.Errorf("%w: semantic extension identity syntax", ErrInvalidInvocation)
		}
	}
	if ext.Direction != "request" && ext.Direction != "response" && ext.Direction != "bidirectional" {
		return fmt.Errorf("%w: semantic extension direction", ErrInvalidInvocation)
	}
	if len(ext.Data.Bytes()) > aipapi.MaxSemanticExtensionDataBytes {
		return fmt.Errorf("%w: semantic extension data exceeds bound", ErrOversizedRawJSON)
	}
	if ext.Presence != SemanticExtensionNull && ext.Presence != SemanticExtensionValue {
		return fmt.Errorf("%w: semantic extension presence", ErrInvalidInvocation)
	}
	if ext.Presence == SemanticExtensionValue && ext.Data.State() != RawJSONValue {
		return fmt.Errorf("%w: semantic extension value data", ErrInvalidInvocation)
	}
	if ext.Presence == SemanticExtensionValue {
		if err := rejectSemanticEnvelope(ext.Data.Bytes()); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidInvocation, err)
		}
	}
	if ext.Presence == SemanticExtensionNull && ext.Data.State() != RawJSONAbsent {
		return fmt.Errorf("%w: semantic extension null data", ErrInvalidInvocation)
	}
	return nil
}

func validateInvocationSemanticAuthority(inv Invocation) error {
	legacy := strings.TrimSpace(inv.PromptCacheKey)
	for _, ext := range inv.SemanticExtensions {
		if ext.Namespace != "aip" || ext.Type != "prompt_cache_key" || ext.Implementor != "proxy" || ext.Direction != "request" {
			continue
		}
		if ext.Presence != SemanticExtensionValue {
			return fmt.Errorf("%w: prompt_cache_key semantic carrier presence", ErrInvalidInvocation)
		}
		var value string
		if err := json.Unmarshal(ext.Data.Bytes(), &value); err != nil {
			return fmt.Errorf("%w: prompt_cache_key semantic carrier value", ErrInvalidInvocation)
		}
		if legacy != "" && legacy != strings.TrimSpace(value) {
			return fmt.Errorf("%w: prompt_cache_key alias conflicts with semantic carrier", ErrInvalidInvocation)
		}
	}
	return nil
}

func rejectSemanticEnvelope(data []byte) error {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	var visit func(any) error
	visit = func(value any) error {
		switch typed := value.(type) {
		case map[string]any:
			for key, child := range typed {
				switch strings.ToLower(key) {
				case "request", "response", "messages", "items", "input", "output", "events", "stream":
					return fmt.Errorf("semantic extension data contains envelope field %q", key)
				}
				if err := visit(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range typed {
				if err := visit(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return visit(value)
}
