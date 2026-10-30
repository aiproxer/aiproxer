package backendplugin

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const (
	MetaOperation          = "aip.operation"
	MetaDeliveryMode       = "aip.delivery_mode"
	MetaTransportMode      = "aip.transport_mode"
	MetaRouteParamPrefix   = "route.param."
	MetaExtensionPrefix    = "ext."
	maxMetaExtensionBytes  = 64 << 10
	maxMetaExtensionFields = 64
)

// ApplyCallWireMetadata projects the legacy-safe operation, delivery, transport,
// route params, ordered item wire fields, and Call.Extensions into an invocation.
// It intentionally does not project proxy-owned session authority: that typed
// field exists only on the negotiated minor-4 ABI.
func ApplyCallWireMetadata(inv *Invocation, call aipapi.Call, routeParams map[string]string) error {
	if err := applyLegacyCallWireMetadata(inv, call, routeParams); err != nil {
		return err
	}
	ApplyOrderedItemWire(inv, call)
	return nil
}

// ApplyCallWireMetadataWithNegotiation projects wire metadata and enforces ordered-item and
// exact OpenResponses ABI gates before execution.
func ApplyCallWireMetadataWithNegotiation(inv *Invocation, call aipapi.Call, routeParams map[string]string, neg Negotiation) error {
	if inv == nil {
		return ErrInvalidInvocation
	}
	if err := RequireOrderedItemABISupport(neg, call); err != nil {
		return err
	}
	if err := RequireExactOpenResponsesABISupport(neg, call); err != nil {
		return err
	}
	if err := RequireSemanticExtensionsABISupport(neg, call); err != nil {
		return err
	}
	if call.HasItemAuthority() {
		if err := checkOrderedItemContentABIRepresentable(call.Items); err != nil {
			return err
		}
	}
	// Session authority is security-sensitive: a negotiated legacy ABI cannot
	// carry it without silently changing the connector's partition boundary.
	// Fail closed instead of allowing the caller to proceed with an unsafe
	// native-context request.
	if strings.TrimSpace(call.Session.AuthoritativeSessionID) != "" && !ProxyOwnedSessionIDSupported(neg) {
		return fmt.Errorf("%w: proxy-owned session authority requires negotiated minor %d and feature %q", ErrProxyOwnedSessionUnsupported, ProtocolMinorProxyOwnedSessionID, FeatureProxyOwnedSessionID)
	}
	if err := applyLegacyCallWireMetadata(inv, call, routeParams); err != nil {
		return err
	}
	ApplyOrderedItemWire(inv, call)
	if ProxyOwnedSemanticExtensionsSupported(neg) {
		if err := applySemanticExtensions(inv, call); err != nil {
			return err
		}
		if len(inv.SemanticExtensions) > 0 {
			inv.PromptCacheKey = ""
		}
	}
	if ProxyOwnedSessionIDSupported(neg) {
		inv.ProxyOwnedSessionID = strings.TrimSpace(call.Session.AuthoritativeSessionID)
	}
	return nil
}

func applySemanticExtensions(inv *Invocation, call aipapi.Call) error {
	var promptCacheKey string
	for _, ext := range call.SemanticExtensions {
		mapped := SemanticExtension{Namespace: ext.Namespace, Type: ext.Type, Implementor: ext.Implementor, Direction: ext.Direction}
		switch ext.Presence {
		case aipapi.SemanticExtensionNull:
			mapped.Presence = SemanticExtensionNull
		case aipapi.SemanticExtensionValue:
			mapped.Presence = SemanticExtensionValue
			mapped.Data = RawJSONFromBytes(ext.Data)
		default:
			return ErrInvalidInvocation
		}
		if err := validateSemanticExtension(mapped); err != nil {
			return err
		}
		if ext.Namespace == "aip" && ext.Type == "prompt_cache_key" && ext.Implementor == "proxy" && ext.Direction == "request" {
			if ext.Presence != aipapi.SemanticExtensionValue {
				return fmt.Errorf("prompt_cache_key semantic carrier must have a value")
			}
			var value string
			if err := json.Unmarshal(ext.Data, &value); err != nil {
				return fmt.Errorf("prompt_cache_key semantic carrier: %w", err)
			}
			promptCacheKey = strings.TrimSpace(value)
		}
		inv.SemanticExtensions = append(inv.SemanticExtensions, mapped)
	}
	legacy := strings.TrimSpace(call.PromptCacheKey)
	if legacy != "" && promptCacheKey != "" && legacy != promptCacheKey {
		return fmt.Errorf("prompt_cache_key legacy alias conflicts with semantic carrier")
	}
	if len(call.SemanticExtensions) == 0 && legacy != "" {
		inv.SemanticExtensions = append(inv.SemanticExtensions, SemanticExtension{
			Namespace: "aip", Type: "prompt_cache_key", Implementor: "proxy", Direction: "request",
			Presence: SemanticExtensionValue, Data: RawJSONFromBytes(fmt.Appendf(nil, "%q", legacy)),
		})
	}
	return nil
}

func applyLegacyCallWireMetadata(inv *Invocation, call aipapi.Call, routeParams map[string]string) error {
	if inv == nil {
		return nil
	}
	if inv.SafeMetadata == nil {
		inv.SafeMetadata = make(map[string]string)
	}
	if op := string(call.Invocation.Operation); op != "" {
		inv.SafeMetadata[MetaOperation] = op
	}
	if dm := string(call.Invocation.DeliveryMode); dm != "" {
		inv.SafeMetadata[MetaDeliveryMode] = dm
	}
	if tm := string(call.Invocation.TransportMode); tm != "" {
		inv.SafeMetadata[MetaTransportMode] = tm
	}
	// Session authority has a typed ABI field, but this helper is deliberately
	// legacy-safe and cannot establish that field or put it in SafeMetadata.
	inv.ProxyOwnedSessionID = ""
	for k, v := range routeParams {
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k == "" || v == "" {
			continue
		}
		inv.SafeMetadata[MetaRouteParamPrefix+k] = v
	}
	// The legacy metadata carrier is bounded. Every extension that does not fit
	// is an explicit refusal, never a silent drop: a provenance or correlation
	// key that disappears here changes the attempt's meaning. The copy is sorted
	// so the same call always produces the same metadata.
	type extension struct {
		key string
		raw json.RawMessage
	}
	items := make([]extension, 0, len(call.Extensions))
	for rawKey, raw := range call.Extensions {
		key := strings.TrimSpace(rawKey)
		if key == "" {
			return fmt.Errorf("%w: extension key is empty", ErrInvalidInvocation)
		}
		if len(raw) == 0 {
			return fmt.Errorf("%w: extension %q has no value", ErrInvalidInvocation, key)
		}
		if len(raw) > maxMetaExtensionBytes {
			return fmt.Errorf("%w: extension %q exceeds the metadata bound", ErrInvalidInvocation, key)
		}
		items = append(items, extension{key: key, raw: raw})
	}
	if len(items) > maxMetaExtensionFields {
		return fmt.Errorf("%w: %d extensions exceed the metadata bound of %d", ErrInvalidInvocation, len(items), maxMetaExtensionFields)
	}
	slices.SortFunc(items, func(left, right extension) int {
		return strings.Compare(left.key, right.key)
	})
	for i, item := range items {
		if i > 0 && items[i-1].key == item.key {
			return fmt.Errorf("%w: duplicate extension key %q", ErrInvalidInvocation, item.key)
		}
		inv.SafeMetadata[MetaExtensionPrefix+item.key] = string(item.raw)
	}
	return nil
}

// RestoreCallWireMetadata rebuilds aipapi.Call wire fields from SafeMetadata.
func RestoreCallWireMetadata(call *aipapi.Call, meta map[string]string) {
	if call == nil || len(meta) == 0 {
		return
	}
	if op := strings.TrimSpace(meta[MetaOperation]); op != "" {
		call.Invocation.Operation = aipapi.Operation(op)
	}
	if dm := strings.TrimSpace(meta[MetaDeliveryMode]); dm != "" {
		call.Invocation.DeliveryMode = aipapi.DeliveryMode(dm)
	}
	if tm := strings.TrimSpace(meta[MetaTransportMode]); tm != "" {
		call.Invocation.TransportMode = aipapi.TransportMode(tm)
	}
	ext := call.Extensions
	if ext == nil {
		ext = make(map[string]json.RawMessage)
	}
	for k, v := range meta {
		if !strings.HasPrefix(k, MetaExtensionPrefix) {
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(k, MetaExtensionPrefix))
		if name == "" || v == "" {
			continue
		}
		ext[name] = json.RawMessage(v)
	}
	if len(ext) > 0 {
		call.Extensions = ext
	}
}

// RouteParam returns a route.param.* value from SafeMetadata.
func RouteParam(meta map[string]string, name string) string {
	name = strings.TrimSpace(name)
	if name == "" || len(meta) == 0 {
		return ""
	}
	return strings.TrimSpace(meta[MetaRouteParamPrefix+name])
}
