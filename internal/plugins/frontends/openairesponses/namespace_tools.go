package openairesponses

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Operator policy for OpenAI Responses namespace tool declarations.
const (
	namespaceToolPolicyProject = "project"
	namespaceToolPolicyReject  = "reject"
)

// namespaceToolNameSeparator joins a namespace and its member for the flat
// function name the backend sees. Double underscore is accepted by the OpenAI
// Chat and Responses function-name grammars; the exact spelling is irrelevant
// because the per-request mapping restores the client identity on output.
const namespaceToolNameSeparator = "__"

// ErrNamespaceToolsRequiresCanonicalDecode reports that the bounded proof pass
// met a raw namespace tool declaration. The proof pass cannot carry the
// frontend's per-request projection, so it declines to canonical decode BEFORE
// parseTools can accept the shape. It must never wire-execute a namespaced
// declaration, independent of the selected backend or the backend's
// WireBackend availability.
var ErrNamespaceToolsRequiresCanonicalDecode = errors.New("openairesponses: namespace tool declarations require canonical decode")

// ErrNamespaceHistoryRequiresCanonicalDecode reports that the bounded proof pass
// met a namespaced function_call in replayed history. The wire path forwards raw
// bytes, while the canonical path maps the item onto the reversible projected
// tool identity (or refuses an ambiguous collision); either way the history must
// not take the wire path without that decision.
var ErrNamespaceHistoryRequiresCanonicalDecode = errors.New("openairesponses: namespace tool history requires canonical decode")

func normalizeNamespaceToolPolicy(raw string) (string, error) {
	switch policy := strings.TrimSpace(raw); policy {
	case "", namespaceToolPolicyProject:
		return namespaceToolPolicyProject, nil
	case namespaceToolPolicyReject:
		return namespaceToolPolicyReject, nil
	default:
		return "", fmt.Errorf("openairesponses: namespace_tools must be %q or %q, got %q", namespaceToolPolicyProject, namespaceToolPolicyReject, raw)
	}
}

// namespaceToolOrigin is one client-authored tool identity. An empty Namespace
// marks an ordinary top-level function tool.
type namespaceToolOrigin struct {
	Namespace string
	Name      string
}

// namespaceToolMapping is the per-request reversible projection between the
// client's namespace/bare-member identities and the flat function names the
// canonical call and every backend use. It is never persisted and never put on
// a wire: decode builds it, encode consumes it for the same request.
type namespaceToolMapping struct {
	byProjected    map[string]namespaceToolOrigin
	byOrigin       map[namespaceToolOrigin]string
	declared       map[string]struct{}
	bareMembers    map[string][]string
	seenNamespaces map[string]struct{}
	namespaceCount int
	memberCount    int
	historyMembers int
}

func newNamespaceToolMapping() *namespaceToolMapping {
	return &namespaceToolMapping{
		byProjected:    make(map[string]namespaceToolOrigin),
		byOrigin:       make(map[namespaceToolOrigin]string),
		declared:       make(map[string]struct{}),
		bareMembers:    make(map[string][]string),
		seenNamespaces: make(map[string]struct{}),
	}
}

// addTopLevel records an ordinary function tool. Duplicate top-level names
// keep their pre-existing behaviour; a top-level name that collides with an
// already projected namespace member is refused because the client could no
// longer tell which tool a model call names.
func (m *namespaceToolMapping) addTopLevel(name string) error {
	if existing, ok := m.byProjected[name]; ok {
		if existing.Namespace != "" {
			return errors.New("tool name is already declared by a namespace member")
		}
		return nil
	}
	m.byProjected[name] = namespaceToolOrigin{Name: name}
	m.declared[name] = struct{}{}
	return nil
}

// addNamespace records a namespace declaration and refuses a duplicate
// namespace name: two declarations would carry different descriptions and
// member sets under one client identity.
func (m *namespaceToolMapping) addNamespace(namespace string) error {
	if _, ok := m.seenNamespaces[namespace]; ok {
		return errors.New("namespace declaration is already declared")
	}
	m.seenNamespaces[namespace] = struct{}{}
	m.namespaceCount++
	return nil
}

// addMember records one namespace member and returns its projected flat name.
func (m *namespaceToolMapping) addMember(namespace, name string) (string, error) {
	projected := namespace + namespaceToolNameSeparator + name
	if _, ok := m.byProjected[projected]; ok {
		return "", errors.New("projected tool name is already declared")
	}
	origin := namespaceToolOrigin{Namespace: namespace, Name: name}
	m.byProjected[projected] = origin
	m.byOrigin[origin] = projected
	m.declared[projected] = struct{}{}
	m.bareMembers[name] = append(m.bareMembers[name], projected)
	m.memberCount++
	return projected, nil
}

// mapHistory returns the reversible projection for a namespaced history item.
// A declaration from this request wins. An undeclared pair is still projected
// deterministically so replayed history is never dropped or silently renamed:
// the request keeps the call id and arguments, and a later model call to the
// same projected name restores the original namespace and bare name. A
// collision with any declared or already mapped name is refused because two
// identities would share one wire name.
func (m *namespaceToolMapping) mapHistory(namespace, name string) (string, error) {
	if m == nil {
		return "", errors.New("namespaced history requires canonical namespace mapping")
	}
	origin := namespaceToolOrigin{Namespace: namespace, Name: name}
	if projected, ok := m.byOrigin[origin]; ok {
		return projected, nil
	}
	projected := namespace + namespaceToolNameSeparator + name
	if len(projected) > aipapi.MaxToolNameBytes {
		return "", fmt.Errorf("projected tool name has %d bytes; maximum is %d", len(projected), aipapi.MaxToolNameBytes)
	}
	if _, ok := m.byProjected[projected]; ok {
		return "", errors.New("projected tool name is already declared")
	}
	m.byProjected[projected] = origin
	m.byOrigin[origin] = projected
	m.historyMembers++
	return projected, nil
}

// resolveToolChoiceName maps a tool_choice function name onto the canonical
// declared tool. An exact declared name always wins. Otherwise a unique bare
// namespace member is accepted; two namespace members with the same bare name
// are ambiguous and refused.
func (m *namespaceToolMapping) resolveToolChoiceName(name string) (string, error) {
	if m == nil {
		return name, nil
	}
	if _, ok := m.declared[name]; ok {
		return name, nil
	}
	switch candidates := m.bareMembers[name]; len(candidates) {
	case 0:
		return name, nil
	case 1:
		return candidates[0], nil
	default:
		return "", errors.New("tool_choice function name matches more than one namespace member")
	}
}

// restore returns the client-authored bare name and namespace for a projected
// model output name. Unknown names and ordinary top-level names pass through.
func (m *namespaceToolMapping) restore(projected string) (string, string) {
	if m == nil {
		return projected, ""
	}
	origin, ok := m.byProjected[projected]
	if !ok || origin.Namespace == "" {
		return projected, ""
	}
	return origin.Name, origin.Namespace
}

func (m *namespaceToolMapping) hasNamespaces() bool {
	return m != nil && (m.namespaceCount > 0 || m.historyMembers > 0)
}

func restoreNamespaceToolName(m *namespaceToolMapping, projected string) (string, string) {
	if m == nil {
		return projected, ""
	}
	return m.restore(projected)
}

// functionToolWire is the shared nested/flat function declaration shape used
// by both top-level function tools and namespace members.
type functionToolWire struct {
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

func parseFunctionToolWire(raw json.RawMessage) (name, description string, parameters json.RawMessage, err error) {
	var w functionToolWire
	if err := json.Unmarshal(raw, &w); err != nil {
		return "", "", nil, err
	}
	name = strings.TrimSpace(w.Function.Name)
	description = w.Function.Description
	parameters = w.Function.Parameters
	if name == "" {
		name = strings.TrimSpace(w.Name)
		if description == "" {
			description = w.Description
		}
		if len(parameters) == 0 {
			parameters = w.Parameters
		}
	}
	return name, description, parameters, nil
}

// namespaceToolWire is one top-level namespace declaration. Member declarations
// are decoded one at a time so a hostile member cannot turn the whole body into
// one unbounded allocation.
type namespaceToolWire struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Tools       []json.RawMessage `json:"tools"`
}

func foldNamespaceToolDescription(namespaceDescription, memberDescription string) string {
	if strings.TrimSpace(namespaceDescription) == "" {
		return memberDescription
	}
	if strings.TrimSpace(memberDescription) == "" {
		return namespaceDescription
	}
	return namespaceDescription + "\n\n" + memberDescription
}

// hasNamespaceToolDeclaration scans raw top-level tools bytes for a namespace
// declaration without calling parseTools. The proof pass uses it to decline
// before a raw namespace shape can reach a wire backend.
func hasNamespaceToolDeclaration(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return false
	}
	for _, item := range items {
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(item, &probe); err != nil {
			continue
		}
		if strings.TrimSpace(probe.Type) == "namespace" {
			return true
		}
	}
	return false
}

// expandNamespaceTool parses one namespace declaration into canonical flat
// function tools. Members keep declaration order and arguments, and each member
// description carries the namespace description.
func expandNamespaceTool(raw json.RawMessage, index int, mapping *namespaceToolMapping, tools *[]aipapi.ToolDef) error {
	var ns namespaceToolWire
	if err := json.Unmarshal(raw, &ns); err != nil {
		return fmt.Errorf("openairesponses: tools[%d]: %w", index, err)
	}
	namespace := strings.TrimSpace(ns.Name)
	if namespace == "" {
		return fmt.Errorf("openairesponses: tools[%d]: namespace name is required", index)
	}
	if err := mapping.addNamespace(namespace); err != nil {
		return fmt.Errorf("openairesponses: tools[%d]: %w", index, err)
	}
	if len(ns.Tools) > aipapi.MaxTools {
		return fmt.Errorf("openairesponses: tools[%d].tools has %d entries; maximum is %d", index, len(ns.Tools), aipapi.MaxTools)
	}
	for memberIndex, memberRaw := range ns.Tools {
		field := fmt.Sprintf("tools[%d].tools[%d]", index, memberIndex)
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(memberRaw, &probe); err != nil {
			return fmt.Errorf("openairesponses: %s: %w", field, err)
		}
		if typ := strings.TrimSpace(probe.Type); typ != "" && typ != "function" {
			return fmt.Errorf("openairesponses: %s: unsupported namespace member type %q", field, typ)
		}
		name, memberDescription, parameters, err := parseFunctionToolWire(memberRaw)
		if err != nil {
			return fmt.Errorf("openairesponses: %s: %w", field, err)
		}
		if name == "" {
			return fmt.Errorf("openairesponses: %s: namespace member name is required", field)
		}
		projected, err := mapping.addMember(namespace, name)
		if err != nil {
			return fmt.Errorf("openairesponses: %s: %w", field, err)
		}
		if len(projected) > aipapi.MaxToolNameBytes {
			return fmt.Errorf("openairesponses: %s: projected tool name has %d bytes; maximum is %d", field, len(projected), aipapi.MaxToolNameBytes)
		}
		description := foldNamespaceToolDescription(ns.Description, memberDescription)
		if len(description) > aipapi.MaxToolDescriptionBytes {
			return fmt.Errorf("openairesponses: %s: folded description has %d bytes; maximum is %d", field, len(description), aipapi.MaxToolDescriptionBytes)
		}
		if len(parameters) == 0 {
			parameters = json.RawMessage(`{}`)
		}
		if len(parameters) > aipapi.MaxToolParametersBytes {
			return fmt.Errorf("openairesponses: %s: tool parameters have %d bytes; maximum is %d", field, len(parameters), aipapi.MaxToolParametersBytes)
		}
		if len(*tools)+1 > aipapi.MaxTools {
			return fmt.Errorf("openairesponses: tools has %d entries after namespace expansion; maximum is %d", len(*tools)+1, aipapi.MaxTools)
		}
		*tools = append(*tools, aipapi.ToolDef{
			Name:        projected,
			Description: description,
			Parameters:  parameters,
		})
	}
	return nil
}
