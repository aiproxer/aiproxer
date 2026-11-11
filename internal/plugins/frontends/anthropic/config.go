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

// Bounded class labels for the reductions applied to a contentless inline
// system carrier. They are constants so an operator notice and any later
// observation keep a closed, content-free vocabulary.
const (
	carrierClassEmptyContent  = "empty_content"
	carrierClassPerTurnEffort = "per_turn_effort_not_forwarded"
	carrierClassMessageFields = "message_fields_not_forwarded"

	perTurnEffortFieldKey = "effort"

	fieldRole    = "role"
	fieldContent = "content"
)

// EmptySystemCarrierReduction reports what one decode did with contentless
// inline system carriers. It never claims the reduction is loss-free: every
// count names a semantic the frontend could not carry onto the canonical call.
//
// This frontend does not forward generation controls in either position: the
// request-level `output_config.effort` is dropped today for every request, with
// or without a carrier (pre-existing behaviour, out of this slice's scope), and
// a carrier's per-turn `output_config.effort` is dropped together with the
// carrier. A per-turn effort is reported whenever the carrier declares one, so
// the notice never implies that a matching request-level value made it safe.
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
func (r EmptySystemCarrierReduction) ClassCounts() []EmptySystemCarrierClassCount {
	out := make([]EmptySystemCarrierClassCount, 0, 3)
	if r.EmptyContent > 0 {
		out = append(out, EmptySystemCarrierClassCount{Class: carrierClassEmptyContent, Count: r.EmptyContent})
	}
	if r.PerTurnEffort > 0 {
		out = append(out, EmptySystemCarrierClassCount{Class: carrierClassPerTurnEffort, Count: r.PerTurnEffort})
	}
	if r.MessageFields > 0 {
		out = append(out, EmptySystemCarrierClassCount{Class: carrierClassMessageFields, Count: r.MessageFields})
	}
	return out
}

// EmptySystemCarrierClassCount is one bounded reduction class with its count.
type EmptySystemCarrierClassCount struct {
	Class string
	Count int
}

// emptySystemCarrierFacts is what one contentless inline system message declared
// beyond its empty content array. Only presence is retained: no effort value or
// field value can reach a notice.
type emptySystemCarrierFacts struct {
	perTurnEffort bool
	otherFields   bool
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
