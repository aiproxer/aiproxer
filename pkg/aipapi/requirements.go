package aipapi

import (
	"slices"
	"strings"
)

// DialectRequirement identifies an exact item, reasoning, or compaction dialect a call requires.
type DialectRequirement struct {
	Kind        string // "item", "reasoning", "compaction"
	Dialect     string
	Implementor string
}

// ExtensionRequirement identifies a namespaced extension type a call requires.
type ExtensionRequirement struct {
	Namespace   string
	Type        string
	Implementor string
}

// ProtocolRequirements carries semantic capability and exact dialect/extension requirements
// derived from a canonical call trajectory.
type ProtocolRequirements struct {
	Capabilities       []Capability
	ItemDialects       []DialectRequirement
	ReasoningDialects  []DialectRequirement
	CompactionDialects []DialectRequirement
	ExtensionTypes     []ExtensionRequirement
}

// DialectSupport declares exact dialects a backend candidate can satisfy.
type DialectSupport struct {
	ItemDialects       []DialectRequirement
	ReasoningDialects  []DialectRequirement
	CompactionDialects []DialectRequirement
	ExtensionTypes     []ExtensionRequirement
}

// NormalizeDialectSupport normalizes dialect support slices deterministically.
func NormalizeDialectSupport(s DialectSupport) DialectSupport {
	return DialectSupport{
		ItemDialects:       normalizeDialectRequirements(s.ItemDialects),
		ReasoningDialects:  normalizeDialectRequirements(s.ReasoningDialects),
		CompactionDialects: normalizeDialectRequirements(s.CompactionDialects),
		ExtensionTypes:     normalizeExtensionRequirements(s.ExtensionTypes),
	}
}

// UnionProtocolRequirements returns the deterministic union of two requirement sets.
// Obligations present in either set are retained; baseline entries from a are never
// weakened by b, and b may add capabilities, dialects, or extensions.
func UnionProtocolRequirements(a, b ProtocolRequirements) ProtocolRequirements {
	return NormalizeProtocolRequirements(ProtocolRequirements{
		Capabilities:       append(append([]Capability(nil), a.Capabilities...), b.Capabilities...),
		ItemDialects:       append(append([]DialectRequirement(nil), a.ItemDialects...), b.ItemDialects...),
		ReasoningDialects:  append(append([]DialectRequirement(nil), a.ReasoningDialects...), b.ReasoningDialects...),
		CompactionDialects: append(append([]DialectRequirement(nil), a.CompactionDialects...), b.CompactionDialects...),
		ExtensionTypes:     append(append([]ExtensionRequirement(nil), a.ExtensionTypes...), b.ExtensionTypes...),
	})
}

// NormalizeProtocolRequirements deduplicates and sorts requirement slices deterministically.
func NormalizeProtocolRequirements(r ProtocolRequirements) ProtocolRequirements {
	out := ProtocolRequirements{
		Capabilities: normalizeCapabilities(r.Capabilities),
	}
	out.ItemDialects = normalizeDialectRequirements(r.ItemDialects)
	out.ReasoningDialects = normalizeDialectRequirements(r.ReasoningDialects)
	out.CompactionDialects = normalizeDialectRequirements(r.CompactionDialects)
	out.ExtensionTypes = normalizeExtensionRequirements(r.ExtensionTypes)
	return out
}

func normalizeCapabilities(in []Capability) []Capability {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[Capability]struct{}, len(in))
	out := make([]Capability, 0, len(in))
	for _, c := range in {
		if c == "" {
			continue
		}
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	slices.Sort(out)
	return out
}

func normalizeDialectRequirements(in []DialectRequirement) []DialectRequirement {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]DialectRequirement, len(in))
	for _, d := range in {
		key := dialectKey(d)
		if key == "" {
			continue
		}
		d.Kind = strings.ToLower(strings.TrimSpace(d.Kind))
		d.Dialect = strings.ToLower(strings.TrimSpace(d.Dialect))
		d.Implementor = strings.ToLower(strings.TrimSpace(d.Implementor))
		seen[key] = d
	}
	out := make([]DialectRequirement, 0, len(seen))
	for _, d := range seen {
		out = append(out, d)
	}
	slices.SortFunc(out, func(a, b DialectRequirement) int {
		if c := strings.Compare(a.Kind, b.Kind); c != 0 {
			return c
		}
		if c := strings.Compare(a.Dialect, b.Dialect); c != 0 {
			return c
		}
		return strings.Compare(a.Implementor, b.Implementor)
	})
	return out
}

func normalizeExtensionRequirements(in []ExtensionRequirement) []ExtensionRequirement {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]ExtensionRequirement, len(in))
	for _, e := range in {
		key := extensionKey(e)
		if key == "" {
			continue
		}
		e.Namespace = strings.TrimSpace(e.Namespace)
		e.Type = strings.ToLower(strings.TrimSpace(e.Type))
		e.Implementor = strings.ToLower(strings.TrimSpace(e.Implementor))
		seen[key] = e
	}
	out := make([]ExtensionRequirement, 0, len(seen))
	for _, e := range seen {
		out = append(out, e)
	}
	slices.SortFunc(out, func(a, b ExtensionRequirement) int {
		if c := strings.Compare(a.Namespace, b.Namespace); c != 0 {
			return c
		}
		if c := strings.Compare(a.Type, b.Type); c != 0 {
			return c
		}
		return strings.Compare(a.Implementor, b.Implementor)
	})
	return out
}

func dialectKey(d DialectRequirement) string {
	kind := strings.ToLower(strings.TrimSpace(d.Kind))
	dialect := strings.ToLower(strings.TrimSpace(d.Dialect))
	if kind == "" || dialect == "" {
		return ""
	}
	return kind + "\x00" + dialect + "\x00" + strings.ToLower(strings.TrimSpace(d.Implementor))
}

func extensionKey(e ExtensionRequirement) string {
	ns := strings.TrimSpace(e.Namespace)
	typ := strings.ToLower(strings.TrimSpace(e.Type))
	if ns == "" || typ == "" {
		return ""
	}
	return ns + "\x00" + typ + "\x00" + strings.ToLower(strings.TrimSpace(e.Implementor))
}

// DeriveExtensionNamespace deterministically derives the namespace of a
// prefixed wire extension discriminator from its leading segment before the
// first ':' or '/'. This mirrors the operator dialect declarations used by
// exact extension admission (namespace "acme" for wire type "acme:widget").
// Types without a separator return the whole trimmed type unchanged.
func DeriveExtensionNamespace(wireType string) string {
	typ := strings.TrimSpace(wireType)
	for i := 0; i < len(typ); i++ {
		if typ[i] == ':' || typ[i] == '/' {
			return typ[:i]
		}
	}
	return typ
}

// extensionRequirementFromContentPart derives the exact ExtensionRequirement of
// a canonical opaque content-part extension. Namespace falls back to the
// deterministic derivation from the prefixed Type when not carried explicitly.
func extensionRequirementFromContentPart(e *ExtensionContentPart) ExtensionRequirement {
	if e == nil {
		return ExtensionRequirement{}
	}
	ns := strings.TrimSpace(e.Namespace)
	if ns == "" {
		ns = DeriveExtensionNamespace(e.Type)
	}
	return ExtensionRequirement{
		Namespace:   ns,
		Type:        e.Type,
		Implementor: e.Implementor,
	}
}

// DeriveProtocolRequirements derives complete protocol requirements from call shape.
func DeriveProtocolRequirements(c Call) ProtocolRequirements {
	req := ProtocolRequirements{
		Capabilities: RequiredCapabilities(c),
	}
	addReasoningDialect := func(d ReasoningDialect) {
		if d == "" {
			return
		}
		req.ReasoningDialects = append(req.ReasoningDialects, DialectRequirement{
			Kind:    "reasoning",
			Dialect: string(NormalizeReasoningDialect(d)),
		})
	}
	addItemDialect := func(dialect, implementor string) {
		// Item dialects describe item-level wire kinds. Content-part extensions
		// intentionally contribute only ExtensionTypes, not ItemDialects.
		dialect = strings.ToLower(strings.TrimSpace(dialect))
		if dialect == "" {
			return
		}
		req.ItemDialects = append(req.ItemDialects, DialectRequirement{
			Kind:        "item",
			Dialect:     dialect,
			Implementor: strings.ToLower(strings.TrimSpace(implementor)),
		})
	}
	addCompactionDialect := func(d, implementor string) {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" {
			return
		}
		req.CompactionDialects = append(req.CompactionDialects, DialectRequirement{
			Kind:        "compaction",
			Dialect:     d,
			Implementor: strings.ToLower(strings.TrimSpace(implementor)),
		})
	}
	addExtension := func(ext *OpaqueExtension) {
		if ext == nil {
			return
		}
		req.Capabilities = append(req.Capabilities, CapabilityOpaqueExtensions)
		req.ExtensionTypes = append(req.ExtensionTypes, ExtensionRequirement{
			Namespace:   ext.Namespace,
			Type:        ext.Type,
			Implementor: ext.Implementor,
		})
	}

	for _, item := range NormalizedItems(c) {
		switch item.Kind {
		case ItemKindItemReference:
			req.Capabilities = append(req.Capabilities, CapabilityItemReferences)
			addItemDialect("item_reference", "")
		case ItemKindCompaction:
			req.Capabilities = append(req.Capabilities, CapabilityCompaction)
			if item.Compaction != nil {
				addCompactionDialect(item.Compaction.Dialect, item.Compaction.Implementor)
				addItemDialect(item.Compaction.Dialect, item.Compaction.Implementor)
			}
		case ItemKindExtension:
			addExtension(item.Extension)
			if item.Extension != nil {
				addItemDialect(item.Extension.Type, item.Extension.Implementor)
			}
		case ItemKindReasoning:
			if item.Reasoning != nil && item.Reasoning.Reasoning != nil {
				addReasoningDialect(item.Reasoning.Reasoning.Dialect)
				addItemDialect(string(NormalizeReasoningDialect(item.Reasoning.Reasoning.Dialect)), "")
			}
		}
		if item.Phase != "" {
			req.Capabilities = append(req.Capabilities, CapabilityAssistantPhase)
		}
		for _, cp := range item.Content {
			switch cp.Kind {
			case ContentPartVideoRef:
				req.Capabilities = append(req.Capabilities, CapabilityVideoInput)
			case ContentPartReasoning:
				if cp.Reasoning != nil {
					addReasoningDialect(cp.Reasoning.Dialect)
				}
			case ContentPartAnnotation:
				req.Capabilities = append(req.Capabilities, CapabilityAnnotations)
			case ContentPartAssistantRef:
				req.Capabilities = append(req.Capabilities, CapabilityAssistantMediaRefs)
			case ContentPartExtension:
				req.Capabilities = append(req.Capabilities, CapabilityOpaqueExtensions)
				req.ExtensionTypes = append(req.ExtensionTypes, extensionRequirementFromContentPart(cp.Extension))
			}
		}
		if item.ToolResult != nil {
			for _, cp := range item.ToolResult.Parts {
				if cp.Kind == ContentPartReasoning && cp.Reasoning != nil {
					addReasoningDialect(cp.Reasoning.Dialect)
				}
				if cp.Kind == ContentPartExtension {
					req.Capabilities = append(req.Capabilities, CapabilityOpaqueExtensions)
					req.ExtensionTypes = append(req.ExtensionTypes, extensionRequirementFromContentPart(cp.Extension))
				}
			}
		}
	}

	if c.HasItemAuthority() {
		req.Capabilities = append(req.Capabilities, CapabilityOrderedItems)
	}
	keys := nonInternalExtensionKeys(c.Extensions)
	slices.Sort(keys)
	for _, key := range keys {
		raw := c.Extensions[key]
		if len(raw) == 0 {
			continue
		}
		req.Capabilities = append(req.Capabilities, CapabilityOpaqueExtensions)
		req.ExtensionTypes = append(req.ExtensionTypes, ExtensionRequirement{
			Namespace: "call",
			Type:      key,
		})
	}
	for _, ext := range c.SemanticExtensions {
		req.Capabilities = append(req.Capabilities, CapabilityOpaqueExtensions)
		req.ExtensionTypes = append(req.ExtensionTypes, ExtensionRequirement{
			Namespace: ext.Namespace, Type: ext.Type, Implementor: ext.Implementor,
		})
	}
	return NormalizeProtocolRequirements(req)
}

// DeriveCandidateRequirements derives admission requirements against the candidate target view.
// When projection applies, semantic obligations are computed from the adapted call shape.
func DeriveCandidateRequirements(call Call, caps BackendCaps, target LegacyProjectionTarget) (ProtocolRequirements, error) {
	if !RequiresProjectionAdaptation(call, caps) {
		return DeriveProtocolRequirements(call), nil
	}
	if call.HasItemAuthority() {
		proj, err := ProjectItemsToLegacyView(call, target)
		if err != nil {
			return ProtocolRequirements{}, err
		}
		adapted := CloneCall(call)
		adapted.Items = nil
		adapted.Instructions = append([]Message(nil), proj.Instructions...)
		adapted.Messages = append([]Message(nil), proj.Messages...)
		return DeriveProtocolRequirements(adapted), nil
	}
	orderedTarget := OrderedItemProjectionTargetFromCaps(caps)
	orderedTarget.SupportedExtensions = append([]ExtensionRequirement(nil), target.SupportedExtensions...)
	orderedTarget.SupportsCallExtensions = target.SupportsOpaqueExtensions
	items, _, err := ProjectLegacyToOrderedItems(call, orderedTarget)
	if err != nil {
		return ProtocolRequirements{}, err
	}
	adapted := CloneCall(call)
	adapted.Messages = nil
	adapted.Instructions = nil
	adapted.Items = append([]Item(nil), items...)
	return DeriveProtocolRequirements(adapted), nil
}

func candidateRequiresLegacyProjection(call Call, caps BackendCaps) bool {
	return RequiresProjectionAdaptation(call, caps) && call.HasItemAuthority() && !capsHas(caps, CapabilityOrderedItems)
}

func stripOrderedItemsAuthorityCap(caps []Capability) []Capability {
	if len(caps) == 0 {
		return nil
	}
	out := make([]Capability, 0, len(caps))
	for _, c := range caps {
		if c == CapabilityOrderedItems {
			continue
		}
		out = append(out, c)
	}
	return normalizeCapabilities(out)
}

func admissionCapabilityRequirements(call Call, caps BackendCaps, target LegacyProjectionTarget, req ProtocolRequirements) []Capability {
	capsReq := append([]Capability(nil), req.Capabilities...)
	if candidateRequiresLegacyProjection(call, caps) {
		capsReq = stripOrderedItemsAuthorityCap(capsReq)
	}
	return capsReq
}

// MatchRequirements compares required protocol requirements against candidate support.
// Every required capability, dialect, and extension must be satisfied; otherwise Kind is reject.
func MatchRequirements(required, supported ProtocolRequirements, replay ReasoningReplaySupport) RequirementsMatchResult {
	required = NormalizeProtocolRequirements(required)
	supportedCaps := NewBackendCaps(supported.Capabilities...)
	missingCaps := []Capability{}
	for _, capability := range required.Capabilities {
		if _, ok := supportedCaps[capability]; !ok {
			missingCaps = append(missingCaps, capability)
		}
	}
	missingReasoning := findMissingDialects(required.ReasoningDialects, supported.ReasoningDialects, replay)
	missingItems := findMissingDialects(required.ItemDialects, supported.ItemDialects, ReasoningReplaySupport{})
	missingCompaction := findMissingDialects(required.CompactionDialects, supported.CompactionDialects, ReasoningReplaySupport{})
	missingAllDialects := append(append([]DialectRequirement{}, missingReasoning...), missingItems...)
	missingAllDialects = append(missingAllDialects, missingCompaction...)
	missingExtensions := missingExtensions(required.ExtensionTypes, supported.ExtensionTypes)

	if len(missingCaps) > 0 || len(missingAllDialects) > 0 || len(missingExtensions) > 0 {
		return RequirementsMatchResult{
			Kind:              NegotiationReject,
			MissingCaps:       missingCaps,
			MissingDialects:   missingAllDialects,
			MissingExtensions: missingExtensions,
		}
	}
	return RequirementsMatchResult{Kind: NegotiationLossless}
}

// RequirementsMatchResult is the outcome of exact protocol requirement matching.
type RequirementsMatchResult struct {
	Kind              NegotiationKind
	MissingCaps       []Capability
	MissingDialects   []DialectRequirement
	MissingExtensions []ExtensionRequirement
}

// Err returns a typed reject error for Kind==NegotiationReject.
func (r RequirementsMatchResult) Err() error {
	if r.Kind != NegotiationReject {
		return nil
	}
	return &RequirementsRejectError{
		MissingCaps:       append([]Capability{}, r.MissingCaps...),
		MissingDialects:   append([]DialectRequirement{}, r.MissingDialects...),
		MissingExtensions: append([]ExtensionRequirement{}, r.MissingExtensions...),
	}
}

func findMissingDialects(required, supported []DialectRequirement, replay ReasoningReplaySupport) []DialectRequirement {
	if len(required) == 0 {
		return nil
	}
	supportedSet := make(map[string]struct{}, len(supported))
	for _, d := range supported {
		supportedSet[dialectKey(d)] = struct{}{}
	}
	replaySet := make(map[ReasoningDialect]struct{}, len(replay.Dialects))
	for _, d := range NormalizeReasoningDialects(replay.Dialects) {
		replaySet[d] = struct{}{}
	}
	var missing []DialectRequirement
	for _, req := range required {
		if _, ok := supportedSet[dialectKey(req)]; ok {
			continue
		}
		if req.Kind == "reasoning" {
			if _, ok := replaySet[ReasoningDialect(req.Dialect)]; ok {
				continue
			}
		}
		missing = append(missing, req)
	}
	return missing
}

func missingExtensions(required, supported []ExtensionRequirement) []ExtensionRequirement {
	if len(required) == 0 {
		return nil
	}
	supportedSet := make(map[string]struct{}, len(supported))
	for _, e := range supported {
		supportedSet[extensionKey(e)] = struct{}{}
	}
	var missing []ExtensionRequirement
	for _, req := range required {
		if _, ok := supportedSet[extensionKey(req)]; ok {
			continue
		}
		missing = append(missing, req)
	}
	return missing
}
