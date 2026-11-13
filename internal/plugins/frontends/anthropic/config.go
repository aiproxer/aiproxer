package anthropic

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	ExposeAipUsageExtensions bool         `yaml:"expose_aip_usage_extensions"`
	ClaudeCodeHybrid         hybridConfig `yaml:"claude_code_hybrid"`
	// EmptySystemMessages selects the operator policy for an inline `role: system`
	// message whose `content` is an empty array. Native Claude Code 2.1.322
	// `--bare` emits that shape, and the vendor SDK documents the message's
	// `output_config.effort` as a per-turn control. This frontend does not forward
	// per-message output configuration, so serving that shape reduces a real
	// semantic; the default `serve` does it explicitly and reports every reduced
	// class, while `reject` refuses the carrier before routing.
	EmptySystemMessages string `yaml:"empty_system_messages"`
	// PerMessageControls selects the operator policy for any inline `role: system`
	// message that declares a per-message control this frontend cannot forward
	// (`output_config` or `clear_at`). The default `serve` reports each reduced
	// class without echoing a client value; `reject` refuses the message before
	// routing. A contentless carrier without controls stays governed by
	// EmptySystemMessages.
	PerMessageControls string `yaml:"per_message_controls"`
}

// Operator policies for an inline system message with an empty content array.
const (
	// EmptySystemMessagesServe serves the request after the reductions the policy
	// allows. Every applied reduction is reported to the operator; none is
	// presented as loss-free.
	EmptySystemMessagesServe = "serve"
	// EmptySystemMessagesReject refuses the carrier, which is the strict
	// wire-conformance behaviour.
	EmptySystemMessagesReject = "reject"
)

func normalizeEmptySystemMessagesPolicy(raw string) (string, error) {
	switch policy := strings.TrimSpace(raw); policy {
	case "", EmptySystemMessagesServe:
		return EmptySystemMessagesServe, nil
	case EmptySystemMessagesReject:
		return EmptySystemMessagesReject, nil
	default:
		return "", fmt.Errorf("anthropic: empty_system_messages must be %q or %q, got %q", EmptySystemMessagesServe, EmptySystemMessagesReject, raw)
	}
}

// Operator policies for an inline system message that declares a per-message
// control this frontend cannot forward.
const (
	// PerMessageControlsServe serves the message after reporting every control
	// the canonical call could not carry.
	PerMessageControlsServe = "serve"
	// PerMessageControlsReject refuses the message before routing.
	PerMessageControlsReject = "reject"
)

func normalizePerMessageControlsPolicy(raw string) (string, error) {
	switch policy := strings.TrimSpace(raw); policy {
	case "", PerMessageControlsServe:
		return PerMessageControlsServe, nil
	case PerMessageControlsReject:
		return PerMessageControlsReject, nil
	default:
		return "", fmt.Errorf("anthropic: per_message_controls must be %q or %q, got %q", PerMessageControlsServe, PerMessageControlsReject, raw)
	}
}

// Bounded class labels for the reductions applied to a contentless inline
// system carrier. They are constants so an operator notice and any later
// observation keep a closed, content-free vocabulary.
const (
	carrierClassEmptyContent  = "empty_content"
	carrierClassPerTurnEffort = "per_turn_effort_not_forwarded"
	carrierClassMessageFields = "message_fields_not_forwarded"

	requestClassEffortNotForwarded = "request_effort_not_forwarded"
	requestClassFieldsNotForwarded = "request_output_config_fields_not_forwarded"

	contentSystemClassPerTurnEffort = "content_system_per_turn_effort_not_forwarded"
	contentSystemClassFields        = "content_system_fields_not_forwarded"

	perTurnEffortFieldKey = "effort"

	fieldRole    = "role"
	fieldContent = "content"
)

// EmptySystemCarrierReduction reports what one decode did with contentless
// inline system carriers. It never claims the reduction is loss-free: every
// count names a semantic the frontend could not carry onto the canonical call.
//
// The request-level `output_config.effort` is carried onto the canonical
// generation options; a carrier's per-turn `output_config.effort` is dropped
// together with the carrier and is reported whenever the carrier declares one,
// so the notice never implies that a matching request-level value made it safe.
type EmptySystemCarrierReduction struct {
	// EmptyContent is the number of contentless carriers removed from the
	// canonical history.
	EmptyContent int
	// PerTurnEffort is the number of removed carriers that declared a
	// `output_config.effort` this frontend does not forward.
	PerTurnEffort int
	// MessageFields is the number of removed carriers that carried any other
	// field this frontend does not model (for example `clear_at`).
	MessageFields int
}

// Applied reports whether any reduction happened at all.
func (r EmptySystemCarrierReduction) Applied() bool {
	return r.EmptyContent > 0 || r.PerTurnEffort > 0 || r.MessageFields > 0
}

// Classes returns the bounded labels of the reductions this decode applied.
func (r EmptySystemCarrierReduction) Classes() []string {
	out := make([]string, 0, 3)
	if r.EmptyContent > 0 {
		out = append(out, carrierClassEmptyContent)
	}
	if r.PerTurnEffort > 0 {
		out = append(out, carrierClassPerTurnEffort)
	}
	if r.MessageFields > 0 {
		out = append(out, carrierClassMessageFields)
	}
	return out
}

// ClassCounts returns the bounded label and count of each reduction class this
// decode applied, in a fixed order. A notice reports one class per line, so a
// class that only appears on a later request still gets its own record.
func (r EmptySystemCarrierReduction) ClassCounts() []ReductionClassCount {
	out := make([]ReductionClassCount, 0, 3)
	if r.EmptyContent > 0 {
		out = append(out, ReductionClassCount{Class: carrierClassEmptyContent, Count: r.EmptyContent})
	}
	if r.PerTurnEffort > 0 {
		out = append(out, ReductionClassCount{Class: carrierClassPerTurnEffort, Count: r.PerTurnEffort})
	}
	if r.MessageFields > 0 {
		out = append(out, ReductionClassCount{Class: carrierClassMessageFields, Count: r.MessageFields})
	}
	return out
}

// ReductionClassCount is one bounded reduction class with its count.
type ReductionClassCount struct {
	Class string
	Count int
}

// RequestControlReduction reports request-level generation controls this
// frontend could not carry onto the canonical call. A usable
// `output_config.effort` is not a reduction: it reaches
// Options.ReasoningEffort. Everything else is counted so the loss is visible.
type RequestControlReduction struct {
	// EffortNotForwarded is set when `output_config.effort` was present but not
	// a non-empty string, so no canonical value could be derived.
	EffortNotForwarded int
	// FieldsNotForwarded counts `output_config` values this frontend does not
	// model: `format`, unknown siblings, or a value that is not an object.
	FieldsNotForwarded int
}

// Applied reports whether any request-level control was not forwarded.
func (r RequestControlReduction) Applied() bool {
	return r.EffortNotForwarded > 0 || r.FieldsNotForwarded > 0
}

// Classes returns the bounded labels of the request-level reductions.
func (r RequestControlReduction) Classes() []string {
	out := make([]string, 0, 2)
	if r.EffortNotForwarded > 0 {
		out = append(out, requestClassEffortNotForwarded)
	}
	if r.FieldsNotForwarded > 0 {
		out = append(out, requestClassFieldsNotForwarded)
	}
	return out
}

// ClassCounts returns the bounded label and count of each request-level
// reduction class, in a fixed order.
func (r RequestControlReduction) ClassCounts() []ReductionClassCount {
	out := make([]ReductionClassCount, 0, 2)
	if r.EffortNotForwarded > 0 {
		out = append(out, ReductionClassCount{Class: requestClassEffortNotForwarded, Count: r.EffortNotForwarded})
	}
	if r.FieldsNotForwarded > 0 {
		out = append(out, ReductionClassCount{Class: requestClassFieldsNotForwarded, Count: r.FieldsNotForwarded})
	}
	return out
}

// ContentSystemControlReduction reports per-message controls declared by a
// content-bearing inline system message. The message keeps its content and its
// position; the controls are not forwarded and are counted here instead.
type ContentSystemControlReduction struct {
	// PerTurnEffort is the number of served content-bearing system messages that
	// declared an `output_config.effort` this frontend does not forward.
	PerTurnEffort int
	// MessageFields is the number of served content-bearing system messages that
	// declared any other field this frontend does not model (for example
	// `clear_at` or an unknown `output_config` sibling).
	MessageFields int
}

// Applied reports whether any content-bearing message declared a control this
// frontend did not forward.
func (r ContentSystemControlReduction) Applied() bool {
	return r.PerTurnEffort > 0 || r.MessageFields > 0
}

// Classes returns the bounded labels of the content-bearing reductions.
func (r ContentSystemControlReduction) Classes() []string {
	out := make([]string, 0, 2)
	if r.PerTurnEffort > 0 {
		out = append(out, contentSystemClassPerTurnEffort)
	}
	if r.MessageFields > 0 {
		out = append(out, contentSystemClassFields)
	}
	return out
}

// ClassCounts returns the bounded label and count of each content-bearing
// reduction class, in a fixed order.
func (r ContentSystemControlReduction) ClassCounts() []ReductionClassCount {
	out := make([]ReductionClassCount, 0, 2)
	if r.PerTurnEffort > 0 {
		out = append(out, ReductionClassCount{Class: contentSystemClassPerTurnEffort, Count: r.PerTurnEffort})
	}
	if r.MessageFields > 0 {
		out = append(out, ReductionClassCount{Class: contentSystemClassFields, Count: r.MessageFields})
	}
	return out
}

// inlineSystemControlFacts is what one inline system message declared beyond
// its content. Only presence is retained: no effort value or field value can
// reach a notice.
type inlineSystemControlFacts struct {
	perTurnEffort bool
	otherFields   bool
}

// declaresAny reports whether the message declared a control this frontend
// cannot forward.
func (f inlineSystemControlFacts) declaresAny() bool {
	return f.perTurnEffort || f.otherFields
}

type hybridConfig struct {
	Enabled        bool          `yaml:"enabled"`
	ExternalModels []hybridModel `yaml:"external_models"`
}

type hybridModel struct {
	Model           string `yaml:"model"`
	Route           string `yaml:"route"`
	RequireSubagent bool   `yaml:"require_subagent"`
}

// A deliberately smaller vocabulary than routing expressions: no aliases,
// annotations, composites, placeholders or whitespace can enter this mode.
func validHybridIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range []byte(s) {
		alnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if !alnum && (i == 0 || !strings.ContainsRune("._/-", rune(c))) {
			return false
		}
	}
	return true
}

func DecodeConfig(n yaml.Node) (Config, error) {
	if n.Kind == yaml.DocumentNode {
		if len(n.Content) == 0 {
			return Config{}, nil
		}
		n = *n.Content[0]
	}
	if n.Kind == 0 {
		return Config{}, nil
	}
	if n.Kind == yaml.ScalarNode && (n.Tag == "!!null" || strings.TrimSpace(n.Value) == "" || n.Value == "null") {
		return Config{}, nil
	}
	if n.Kind != yaml.MappingNode {
		return Config{}, fmt.Errorf("anthropic: config must be a mapping or null")
	}
	raw, err := yaml.Marshal(&n)
	if err != nil {
		return Config{}, fmt.Errorf("anthropic: invalid config")
	}
	d := yaml.NewDecoder(bytes.NewReader(raw))
	d.KnownFields(true)
	var cfg Config
	if err := d.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("anthropic: invalid or unknown config key")
	}
	if cfg.EmptySystemMessages, err = normalizeEmptySystemMessagesPolicy(cfg.EmptySystemMessages); err != nil {
		return Config{}, err
	}
	if cfg.PerMessageControls, err = normalizePerMessageControlsPolicy(cfg.PerMessageControls); err != nil {
		return Config{}, err
	}
	seen := map[string]bool{}
	for _, m := range cfg.ClaudeCodeHybrid.ExternalModels {
		backend, model, ok := strings.Cut(m.Route, ":")
		if len(m.Model) > 256 || !validHybridIdentifier(m.Model) || strings.HasPrefix(strings.ToLower(m.Model), "claude-") || seen[m.Model] {
			return Config{}, fmt.Errorf("anthropic: invalid, native-colliding or duplicate hybrid model")
		}
		if !ok || len(m.Route) > 512 || !validHybridIdentifier(backend) || !validHybridIdentifier(model) {
			return Config{}, fmt.Errorf("anthropic: hybrid route must be an explicit single backend:model leaf")
		}
		seen[m.Model] = true
	}
	if cfg.ClaudeCodeHybrid.Enabled && len(seen) == 0 {
		return Config{}, fmt.Errorf("anthropic: hybrid mode requires an external model")
	}
	return cfg, nil
}
