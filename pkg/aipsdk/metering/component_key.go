package metering

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// Component identity bounds are deliberately independent of transport frame
	// limits. Adapters may negotiate lower limits but may not widen these ones.
	MaxComponentNameBytes  = 256
	MaxUnitNameBytes       = 128
	MaxSchemaIDBytes       = 512
	MaxDimensionNameBytes  = 64
	MaxDimensionValueBytes = 256
	MaxDimensions          = 16
	// MaxComponentSchemaRelationships keeps schema-declared graph input
	// bounded without adding a separate batch-size policy. It is aligned with
	// the existing per-observation component-entry bound.
	MaxComponentSchemaRelationships = 128
	// MaxComponentSchemas bounds the frozen schema set published with one
	// rating snapshot. It is independent of the per-schema relationship bound
	// so nested graphs stay reviewable without an unbounded publication size.
	MaxComponentSchemas = 64
)

var (
	ErrInvalidComponentKey    = errors.New("metering: invalid component key")
	ErrInvalidComponentSchema = errors.New("metering: invalid component schema")
	// ErrInvalidUnit is wrapped when a unit is unknown or incompatible with a
	// registered component.
	ErrInvalidUnit = errors.New("metering: invalid unit")
	// ErrInvalidSchema is wrapped when a schema identifier is required or
	// malformed.
	ErrInvalidSchema = errors.New("metering: invalid schema")
)

// FlowDirection is the direction of a flow-valued measure. Subject scope is
// intentionally orthogonal: resource and account/window economics use
// DirectionNone plus a subject/component identity, never a fake direction.
type FlowDirection string

// EconomicDirection is retained as a source-compatible alias for the parent
// design's name. It has the refined three-value meaning of FlowDirection.
type EconomicDirection = FlowDirection

const (
	DirectionNone   FlowDirection = "none"
	DirectionInput  FlowDirection = "input"
	DirectionOutput FlowDirection = "output"
)

func (d FlowDirection) IsKnown() bool {
	switch d {
	case DirectionNone, DirectionInput, DirectionOutput:
		return true
	default:
		return false
	}
}

func (d FlowDirection) Validate() error {
	if !d.IsKnown() {
		return fmt.Errorf("%w: unknown direction %q", ErrInvalidComponentKey, d)
	}
	return nil
}

// Native units used by current and anticipated provider families. A
// schema-qualified unknown unit is also valid, so this list is not a closed
// provider catalog.
const (
	UnitImage           = "image"
	UnitAudio           = "audio"
	UnitVideo           = "video"
	UnitDocument        = "document"
	UnitFile            = "file"
	UnitPage            = "page"
	UnitTile            = "tile"
	UnitSecond          = "second"
	UnitMillisecond     = "millisecond"
	UnitMinute          = "minute"
	UnitHour            = "hour"
	UnitFrame           = "frame"
	UnitPixel           = "pixel"
	UnitMegapixel       = "megapixel"
	UnitMegapixelSecond = "megapixel_second"
	UnitByte            = "byte"
	UnitByteSecond      = "byte_second"
	UnitQuery           = "query"
	UnitCredit          = "credit"
	UnitPercent         = "percent"
	UnitTokenSecond     = "token_second"
)

// Common multimodal component names are vocabulary, not a closed provider
// registry. Namespaced additions remain valid when a schema is supplied.
const (
	ComponentTextToken          = "text_token"
	ComponentInputTokenUncached = "input_token_uncached"
	ComponentInputTokenTotal    = "input_token_total"
	ComponentImage              = "image"
	ComponentImageToken         = "image_token"
	ComponentAudio              = "audio"
	ComponentAudioToken         = "audio_token"
	ComponentVideo              = "video"
	ComponentVideoToken         = "video_token"
	ComponentDocument           = "document"
	ComponentDocumentToken      = "document_token"
	ComponentFile               = "file"
	ComponentToolQuery          = "tool_query"
	ComponentCredit             = "credit"
	ComponentStorage            = "cache_storage"
)

var componentKeyUnits = map[string]string{
	ComponentRequest:              UnitCount,
	ComponentInputToken:           UnitToken,
	ComponentInputTokenUncached:   UnitToken,
	ComponentInputTokenTotal:      UnitToken,
	ComponentOutputToken:          UnitToken,
	ComponentCacheReadInputToken:  UnitToken,
	ComponentCacheWriteInputToken: UnitToken,
	ComponentReasoningOutputToken: UnitToken,
	ComponentTotalToken:           UnitToken,
	ComponentTextToken:            UnitToken,
	ComponentImageToken:           UnitToken,
	ComponentAudioToken:           UnitToken,
	ComponentVideoToken:           UnitToken,
	ComponentDocumentToken:        UnitToken,
	ComponentToolQuery:            UnitCount,
	ComponentCredit:               UnitCredit,
	ComponentStorage:              UnitByteSecond,
}

// Dimension is a bounded price-relevant qualifier. Names are unique within a
// ComponentKey; arbitrary customer text is not a supported qualifier.
type Dimension struct {
	Name  string `json:"name" yaml:"name"`
	Value string `json:"value" yaml:"value"`
}

func (d Dimension) Validate() error {
	if err := validateIdentityText("dimension name", d.Name, MaxDimensionNameBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidComponentKey, err)
	}
	if err := validateIdentityText("dimension value", d.Value, MaxDimensionValueBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidComponentKey, err)
	}
	return nil
}

// ComponentKey is the complete canonical identity of one economic measure.
// Direction, unit and schema are all significant, while dimensions are a
// sorted set for identity purposes.
type ComponentKey struct {
	Direction  FlowDirection `json:"direction" yaml:"direction"`
	Component  string        `json:"component" yaml:"component"`
	Unit       string        `json:"unit" yaml:"unit"`
	SchemaID   string        `json:"schema_id,omitempty" yaml:"schema_id,omitempty"`
	Dimensions []Dimension   `json:"dimensions,omitempty" yaml:"dimensions,omitempty"`
}

// Validate checks bounded identity and contradiction rules without changing
// caller-owned slices. Dimension order is not significant to validation.
func (k ComponentKey) Validate() error {
	if err := k.Direction.Validate(); err != nil {
		return err
	}
	if err := validateIdentityText("component", k.Component, MaxComponentNameBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidComponentKey, err)
	}
	if err := validateIdentityText("unit", k.Unit, MaxUnitNameBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidComponentKey, err)
	}
	if k.SchemaID != "" {
		if err := validateIdentityText("schema_id", k.SchemaID, MaxSchemaIDBytes); err != nil {
			return fmt.Errorf("%w: %w: %v", ErrInvalidComponentKey, ErrInvalidSchema, err)
		}
	}
	if !isKnownUnit(k.Unit) && k.SchemaID == "" {
		return fmt.Errorf("%w: %w: unknown unit %q requires schema_id", ErrInvalidComponentKey, ErrInvalidUnit, k.Unit)
	}
	if want, ok := registeredUnits[k.Component]; ok && k.Unit != want {
		return fmt.Errorf("%w: %w: component %q requires unit %q, got %q", ErrInvalidComponentKey, ErrInvalidUnit, k.Component, want, k.Unit)
	}
	if want, ok := componentKeyUnits[k.Component]; ok && k.Unit != want {
		return fmt.Errorf("%w: %w: component %q requires unit %q, got %q", ErrInvalidComponentKey, ErrInvalidUnit, k.Component, want, k.Unit)
	}
	if !isKnownComponent(k.Component) && k.SchemaID == "" {
		return fmt.Errorf("%w: %w: unknown component %q requires schema_id", ErrInvalidComponentKey, ErrInvalidSchema, k.Component)
	}
	if err := validateDirectionComponent(k.Direction, k.Component); err != nil {
		return err
	}
	if len(k.Dimensions) > MaxDimensions {
		return fmt.Errorf("%w: dimensions exceed %d", ErrInvalidComponentKey, MaxDimensions)
	}
	seen := make(map[string]struct{}, len(k.Dimensions))
	for i, dimension := range k.Dimensions {
		if err := dimension.Validate(); err != nil {
			return fmt.Errorf("%w: dimensions[%d]: %v", ErrInvalidComponentKey, i, err)
		}
		if _, ok := seen[dimension.Name]; ok {
			return fmt.Errorf("%w: duplicate dimension name %q", ErrInvalidComponentKey, dimension.Name)
		}
		seen[dimension.Name] = struct{}{}
	}
	return nil
}

func validateDirectionComponent(direction FlowDirection, component string) error {
	switch component {
	case ComponentRequest:
		if direction != DirectionNone {
			return fmt.Errorf("%w: request component must use direction none", ErrInvalidComponentKey)
		}
	case ComponentTextToken, ComponentImage, ComponentImageToken, ComponentAudio, ComponentAudioToken,
		ComponentVideo, ComponentVideoToken, ComponentDocument, ComponentDocumentToken, ComponentFile:
		if direction != DirectionInput && direction != DirectionOutput {
			return fmt.Errorf("%w: %s requires input or output direction", ErrInvalidComponentKey, component)
		}
	case ComponentInputToken, ComponentInputTokenUncached, ComponentInputTokenTotal, ComponentCacheReadInputToken, ComponentCacheWriteInputToken:
		if direction != DirectionInput {
			return fmt.Errorf("%w: %s requires input direction", ErrInvalidComponentKey, component)
		}
	case ComponentOutputToken, ComponentReasoningOutputToken:
		if direction != DirectionOutput {
			return fmt.Errorf("%w: %s requires output direction", ErrInvalidComponentKey, component)
		}
	case ComponentToolQuery, ComponentCredit, ComponentStorage:
		if direction != DirectionNone {
			return fmt.Errorf("%w: %s requires direction none", ErrInvalidComponentKey, component)
		}
	case ComponentTotalToken:
		// total_token is an aggregate across input/output and may therefore use
		// none only; directional totals use a distinct schema-qualified component.
		if direction != DirectionNone {
			return fmt.Errorf("%w: total_token requires direction none", ErrInvalidComponentKey)
		}
	}
	return nil
}

func isKnownUnit(unit string) bool {
	switch unit {
	case UnitCount, UnitToken, UnitImage, UnitAudio, UnitVideo, UnitDocument, UnitFile,
		UnitPage, UnitTile, UnitSecond, UnitMillisecond, UnitMinute, UnitHour, UnitFrame, UnitPixel, UnitMegapixel,
		UnitMegapixelSecond, UnitByte, UnitByteSecond, UnitQuery, UnitCredit, UnitPercent, UnitTokenSecond:
		return true
	default:
		return false
	}
}

func isKnownComponent(component string) bool {
	if IsRegisteredComponent(component) {
		return true
	}
	switch component {
	case ComponentTextToken, ComponentInputTokenUncached, ComponentInputTokenTotal,
		ComponentImage, ComponentImageToken, ComponentAudio, ComponentAudioToken,
		ComponentVideo, ComponentVideoToken, ComponentDocument, ComponentDocumentToken,
		ComponentFile, ComponentToolQuery, ComponentCredit, ComponentStorage:
		return true
	default:
		return false
	}
}

// Normalize returns a deep-copied key with dimensions sorted by name. It
// rejects duplicate names and never silently trims or rewrites identity.
func (k ComponentKey) Normalize() (ComponentKey, error) {
	if err := k.Validate(); err != nil {
		return ComponentKey{}, err
	}
	out := k.Clone()
	slices.SortFunc(out.Dimensions, func(a, b Dimension) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		if a.Value < b.Value {
			return -1
		}
		if a.Value > b.Value {
			return 1
		}
		return 0
	})
	return out, nil
}

// Clone returns a deep copy of k.
func (k ComponentKey) Clone() ComponentKey {
	k.Dimensions = append([]Dimension(nil), k.Dimensions...)
	return k
}

// CanonicalBytes returns deterministic JSON for a normalized key. It is safe
// to use as a content-addressing preimage because dimensions are sorted and
// all field names are explicit.
func (k ComponentKey) CanonicalBytes() []byte {
	b, err := k.CanonicalJSON()
	if err != nil {
		return nil
	}
	return b
}

// CanonicalJSON is the error-returning form of CanonicalBytes.
func (k ComponentKey) CanonicalJSON() ([]byte, error) {
	n, err := k.Normalize()
	if err != nil {
		return nil, err
	}
	return json.Marshal(componentKeyWire(n))
}

// MarshalJSON keeps direct ComponentKey encoding on the same canonical path
// as observation and valuation serializers. Callers that need the validation
// error should use CanonicalJSON.
func (k ComponentKey) MarshalJSON() ([]byte, error) { return k.CanonicalJSON() }

type componentKeyWire ComponentKey

// CanonicalKey returns the UTF-8 canonical JSON identity. Invalid keys return
// an empty string; use CanonicalJSON when the validation error is required.
func (k ComponentKey) CanonicalKey() string { return string(k.CanonicalBytes()) }

// Fingerprint returns SHA-256 over the canonical key JSON. Invalid keys return
// an empty fingerprint rather than hashing an unsafe representation.
func (k ComponentKey) Fingerprint() string {
	b := k.CanonicalBytes()
	if len(b) == 0 {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Hash is an explicit alias for Fingerprint used by storage adapters.
func (k ComponentKey) Hash() string { return k.Fingerprint() }

// Equal compares normalized keys, so dimension order does not matter.
func (k ComponentKey) Equal(other ComponentKey) bool {
	a, errA := k.Normalize()
	b, errB := other.Normalize()
	if errA != nil || errB != nil {
		return false
	}
	return a.Direction == b.Direction && a.Component == b.Component && a.Unit == b.Unit && a.SchemaID == b.SchemaID && slices.Equal(a.Dimensions, b.Dimensions)
}

// RelationshipKind declares how a schema relates two component keys. The
// relationship is explicit; component names alone never imply inclusion.
//
// RelationshipAggregate and RelationshipPartition are SYNONYMS: both declare
// that the declared children form a COMPLETE, additive coverage of the parent
// (the parent quantity equals the sum of the declared child quantities), and
// both are therefore treated identically by every consumer. RelationshipSubset
// declares only PARTIAL containment (child <= parent, no conservation claim).
// RelationshipTransform is a separately governed unit derivation and is never
// containment.
type RelationshipKind string

const (
	RelationshipAggregate RelationshipKind = "aggregate"
	RelationshipSubset    RelationshipKind = "subset"
	RelationshipPartition RelationshipKind = "partition"
	RelationshipTransform RelationshipKind = "transform"
)

func (k RelationshipKind) IsKnown() bool {
	switch k {
	case RelationshipAggregate, RelationshipSubset, RelationshipPartition, RelationshipTransform:
		return true
	default:
		return false
	}
}

func (k RelationshipKind) Validate() error {
	if !k.IsKnown() {
		return fmt.Errorf("%w: unknown relationship %q", ErrInvalidComponentSchema, k)
	}
	return nil
}

// ComponentRelationship is one schema-declared parent/child or transform
// edge. Parent and child may retain different native units only for an
// explicitly declared transform relationship.
//
// Optional marks a member of a complete aggregate/partition coverage that the
// provider may omit. When the member is present it is a required part of the
// coverage and must be complete and rateable; when it is absent the parent's
// coverage is still proven by the remaining members. It is therefore the
// truthful encoding of a wire family that reports a disjoint detail member
// conditionally, keeping absence distinct from an explicit provider zero.
// Optional is only meaningful for an aggregate or partition relationship; a
// subset is already a partial containment and a transform is a separately
// governed unit derivation.
type ComponentRelationship struct {
	Kind     RelationshipKind `json:"kind"`
	Parent   ComponentKey     `json:"parent"`
	Child    ComponentKey     `json:"child"`
	Optional bool             `json:"optional,omitempty"`
}

func (r ComponentRelationship) Validate() error {
	if err := r.Kind.Validate(); err != nil {
		return err
	}
	if err := r.Parent.Validate(); err != nil {
		return fmt.Errorf("%w: parent: %v", ErrInvalidComponentSchema, err)
	}
	if err := r.Child.Validate(); err != nil {
		return fmt.Errorf("%w: child: %v", ErrInvalidComponentSchema, err)
	}
	if r.Parent.Equal(r.Child) {
		return fmt.Errorf("%w: relationship cannot reference itself", ErrInvalidComponentSchema)
	}
	if r.Kind != RelationshipTransform && r.Parent.Unit != r.Child.Unit {
		return fmt.Errorf("%w: %s relationship requires equal units", ErrInvalidComponentSchema, r.Kind)
	}
	if r.Optional && r.Kind != RelationshipAggregate && r.Kind != RelationshipPartition {
		return fmt.Errorf("%w: optional membership requires an aggregate or partition relationship", ErrInvalidComponentSchema)
	}
	return nil
}

// ComponentSchema declares versioned inclusion/partition/transform semantics.
type ComponentSchema struct {
	ID            string                  `json:"id"`
	Version       string                  `json:"version"`
	Relationships []ComponentRelationship `json:"relationships,omitempty"`
}

func (s ComponentSchema) Validate() error {
	if err := validateIdentityText("schema id", s.ID, MaxSchemaIDBytes); err != nil {
		return fmt.Errorf("%w: %w: %v", ErrInvalidComponentSchema, ErrInvalidSchema, err)
	}
	if err := validateIdentityText("schema version", s.Version, MaxSchemaIDBytes); err != nil {
		return fmt.Errorf("%w: %w: %v", ErrInvalidComponentSchema, ErrInvalidSchema, err)
	}
	if len(s.Relationships) > MaxComponentSchemaRelationships {
		return fmt.Errorf("%w: relationships exceed %d", ErrInvalidComponentSchema, MaxComponentSchemaRelationships)
	}
	seen := make(map[string]struct{}, len(s.Relationships))
	for i, r := range s.Relationships {
		if err := r.Validate(); err != nil {
			return fmt.Errorf("%w: relationships[%d]: %v", ErrInvalidComponentSchema, i, err)
		}
		key := r.Kind.String() + "\x00" + r.Parent.CanonicalKey() + "\x00" + r.Child.CanonicalKey()
		if _, ok := seen[key]; ok {
			return fmt.Errorf("%w: duplicate relationship", ErrInvalidComponentSchema)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// Clone returns a deep copy of r, including both component keys.
func (r ComponentRelationship) Clone() ComponentRelationship {
	out := r
	out.Parent = r.Parent.Clone()
	out.Child = r.Child.Clone()
	return out
}

// Clone returns a deep copy of s, including relationship keys and dimensions.
func (s ComponentSchema) Clone() ComponentSchema {
	out := s
	if s.Relationships != nil {
		out.Relationships = make([]ComponentRelationship, len(s.Relationships))
		for i, relationship := range s.Relationships {
			out.Relationships[i] = relationship.Clone()
		}
	}
	return out
}

// enforcedSchemaTopologyRules is the single place that decides which topology
// rules publication-time validation rejects. It is a function so package state
// cannot be mutated, and it is the only place a rule is promoted to hard
// rejection.
//
// SchemaTopologyCompleteSiblingSharedDescendant is enforced. Its blast radius
// over the generated 7^3 = 343 three-node topology space is 0 (it is
// unreachable there by construction: two complete siblings of one parent cannot
// both reach a third node with only three forward nodes), it reports nothing on
// the shipped OpenAI production schema, and it breaks no test in the
// repository, so publication can refuse it outright.
//
// SchemaTopologyDirectionMismatch is reported but NOT enforced. A cross-direction
// containment edge is inert rather than unsatisfiable, and the historical
// publication contract accepts it: a cross-direction subset and a
// cross-direction partition are both published by pinned regressions that
// require the edge to validate and then be ignored. Rejecting it would
// invalidate those publications.
//
// SchemaTopologyCompleteSiblingContains is ENFORCED. Two complete children of one
// parent, where one transitively contains the other, cannot be a disjoint
// additive sum, so the topology is unsatisfiable regardless of the evidence. It
// rejects 80 of the 343 generated three-node topologies and passes the shipped
// OpenAI production schema; the only test it affected was the order-invariance
// enumeration, which now skips publication-rejected graphs the same way the
// structure and commercial sweeps already do.
func enforcedSchemaTopologyRules() []SchemaTopologyRule {
	return []SchemaTopologyRule{
		SchemaTopologyCompleteSiblingSharedDescendant,
		SchemaTopologyCompleteSiblingContains,
	}
}

// validateComponentSchemaStructure runs every schema check that does not depend
// on a topology rule: the per-schema edge bound, self-reference, unit equality
// and optional-membership rules, then the set-level bounds, unique schema ids,
// single-meaning ordered parent/child pairs and acyclicity. Nil or empty input
// is valid and preserves legacy publication identity.
func validateComponentSchemaStructure(schemas []ComponentSchema) error {
	if len(schemas) > MaxComponentSchemas {
		return fmt.Errorf("%w: schema bound exceeds %d", ErrInvalidComponentSchema, MaxComponentSchemas)
	}
	ids := make(map[string]struct{}, len(schemas))
	pairs := make(map[string]struct{})
	edges := make(map[string]map[string]struct{})
	for i, schema := range schemas {
		if err := schema.Validate(); err != nil {
			return fmt.Errorf("%w: schemas[%d]: %v", ErrInvalidComponentSchema, i, err)
		}
		if _, exists := ids[schema.ID]; exists {
			return fmt.Errorf("%w: duplicate schema id %q", ErrInvalidComponentSchema, schema.ID)
		}
		ids[schema.ID] = struct{}{}
		for _, relationship := range schema.Relationships {
			parent := relationship.Parent.CanonicalKey()
			child := relationship.Child.CanonicalKey()
			pair := parent + "\x00" + child
			if _, exists := pairs[pair]; exists {
				return fmt.Errorf("%w: ambiguous duplicate relationship between parent %s and child %s", ErrInvalidComponentSchema, parent, child)
			}
			pairs[pair] = struct{}{}
			if edges[parent] == nil {
				edges[parent] = make(map[string]struct{})
			}
			edges[parent][child] = struct{}{}
		}
	}
	return validateComponentRelationshipAcyclic(edges)
}

// ValidateComponentSchemas validates a bounded, frozen set of component
// schemas. Beyond each schema's own edge bound and self/unit rules it enforces
// set-level semantics: schema IDs are unique, relationship edges form an
// acyclic directed graph, and no ordered parent/child pair is declared with
// more than one meaning. It additionally rejects every structurally-knowable
// containment impossibility listed in EnforcedComponentSchemaTopologyRules, so
// an unsatisfiable complete coverage is refused at publication instead of being
// rediscovered per rated call. Nil or empty input is valid and preserves legacy
// publication identity.
//
// To choose a different topology gate, or to report findings without rejecting
// them, use ValidateComponentSchemasWithTopology and
// InspectComponentSchemaTopology.
func ValidateComponentSchemas(schemas []ComponentSchema) error {
	return ValidateComponentSchemasWithTopology(schemas, EnforcedComponentSchemaTopologyRules())
}

// validateComponentRelationshipAcyclic rejects any cycle reachable from the
// declared parent/child edges. Inclusion, partition and transform relationships
// are all directed containment/derivation edges, so a cycle is always an
// ambiguous component identity rather than a valid graph.
func validateComponentRelationshipAcyclic(edges map[string]map[string]struct{}) error {
	const (
		unvisited = 0
		visiting  = 1
		done      = 2
	)
	state := make(map[string]int, len(edges))
	var visit func(node string) error
	visit = func(node string) error {
		switch state[node] {
		case visiting:
			return fmt.Errorf("%w: relationship cycle at %s", ErrInvalidComponentSchema, node)
		case done:
			return nil
		}
		state[node] = visiting
		children := make([]string, 0, len(edges[node]))
		for child := range edges[node] {
			children = append(children, child)
		}
		slices.Sort(children)
		for _, child := range children {
			if err := visit(child); err != nil {
				return err
			}
		}
		state[node] = done
		return nil
	}
	nodes := make([]string, 0, len(edges))
	for node := range edges {
		nodes = append(nodes, node)
	}
	slices.Sort(nodes)
	for _, node := range nodes {
		if err := visit(node); err != nil {
			return err
		}
	}
	return nil
}

func (k RelationshipKind) String() string { return string(k) }

func validateIdentityText(field, value string, maxBytes int) error {
	if value == "" {
		return fmt.Errorf("%s required", field)
	}
	if len(value) > maxBytes {
		return fmt.Errorf("%s exceeds %d bytes", field, maxBytes)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", field)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f || !unicode.IsPrint(r) {
			return fmt.Errorf("%s contains unsafe characters", field)
		}
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must not have surrounding whitespace", field)
	}
	return nil
}
