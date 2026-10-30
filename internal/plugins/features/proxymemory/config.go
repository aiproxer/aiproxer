package proxymemory

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ID is the feature registration identity.
const ID = "proxy-memory"

const (
	defaultRecallTimeout      = 50 * time.Millisecond
	defaultMinIdle            = 6 * time.Hour
	defaultMaxSessionAge      = 240 * time.Hour
	defaultMaxUnusedAge       = 720 * time.Hour
	defaultMaxRawMemories     = 256
	defaultPollInterval       = 5 * time.Minute
	defaultPhase1Batch        = 8
	defaultPhase2Batch        = 4
	defaultPhase1InputTokens  = 150000
	defaultPhase2InputTokens  = 150000
	defaultPhase2NoteBytes    = 65536
	defaultSummaryTokens      = 2500
	defaultMaxTranscriptBytes = 8388608
	defaultLease              = time.Hour
	defaultRetryBackoff       = time.Hour
	defaultAuxTimeout         = 10 * time.Minute
	defaultMaxOutputBytes     = 1048576
	defaultMaxNoteBytes       = 8192
	defaultMaxReadBytes       = 65536
	defaultMaxSearchResults   = 50

	// summaryTokensLimit keeps the rendered overlay under 64 KiB.
	summaryTokensLimit = 4000
	// maxTranscriptBytesLimit caps one stored transcript at 64 MiB.
	maxTranscriptBytesLimit = 64 << 20
	// readTruncationSeparator ends the content lines of a size-limited read so
	// the truncation marker starts a line of its own (service.go cutReadOutput).
	readTruncationSeparator = "\n"
	// minReadBytesLimit is the smallest MaxReadBytes that can carry the complete
	// truncation marker: the separator newline entering the final marker line
	// plus the marker line itself (service.go cutReadOutput). The design §11.1
	// read cap bounds the whole response, including that final line, so a
	// positive limit below this floor can never honour the marker contract;
	// Validate rejects it, and the read cut keeps a belt for direct misuse.
	minReadBytesLimit = len(readTruncationSeparator) + len(readTruncatedLine)
)

// Config is the feature configuration subtree. Enablement belongs to the outer
// feature registration; DecodeConfig applies every documented default for the
// keys the operator omits.
type Config struct {
	Principals          []string // required, non-empty; "*" alone means every eligible principal
	AllowLocalPrincipal bool     // default false
	ExtractRoute        string   // required route selector
	ConsolidateRoute    string   // required route selector
	// Models is the per-phase model policy: the target each phase runs with
	// by default and the inventory an owner may choose from (design §3).
	Models ModelPolicy
	// ModelsDeclared reports whether the operator declared the per-phase
	// model policy. A configuration that declares none keeps the legacy
	// behaviour: the phase dispatches with the operator's route and today's
	// effort, and no target is compiled.
	ModelsDeclared bool
	// TargetCompiler is the generation-bound port that compiles a phase
	// target into the final compiled actual target (design §1). The
	// composition root binds it per generation; it is never decoded from
	// the configuration. A configuration that declares a model policy
	// refuses its phases explicitly while no port is bound.
	TargetCompiler TargetCompiler
	// FundingMode is the operator-declared funding decision for this
	// feature's background provider calls. It is a configuration vocabulary
	// only: the host owns the funding machinery, the feature never resolves
	// an account and never decides who pays. The empty mode is undeclared,
	// and an undeclared mode leaves the background work refused.
	FundingMode FundingMode
	// SponsorKeys are the sponsor keys the operator declared for this
	// feature's background work. A key names a sponsor, never an account.
	SponsorKeys []string
	// SponsorKey is the selected sponsor key. It must be one of the declared
	// SponsorKeys when it is set.
	SponsorKey string
	// SQLitePath is the proxy-memory database file. The memory database is
	// always separate from the continuity database, so a memory write can
	// never hold the mandatory core connection. An empty path selects the
	// composition root's default file next to the continuity database.
	SQLitePath         string        // default: the composition root's default
	MinIdle            time.Duration // default 6h      (Codex min_rollout_idle_hours)
	RecallTimeout      time.Duration // default 50ms; bounds the complete read-only first-turn recall
	MaxSessionAge      time.Duration // default 240h    (Codex max_rollout_age_days=10)
	MaxUnusedAge       time.Duration // default 720h    (Codex max_unused_days=30)
	MaxRawMemories     int           // default 256     (Codex max_raw_memories_for_consolidation)
	PollInterval       time.Duration // default 5m
	Phase1Batch        int           // default 8: max sessions processed per tick, one claim at a time
	Phase2Batch        int           // default 4: max banks processed per tick, one claim at a time
	Phase1InputTokens  int           // default 150000  (Codex DEFAULT_ROLLOUT_TOKEN_LIMIT)
	Phase2InputTokens  int           // default 150000
	Phase2NoteBytes    int           // default 65536  (notes sent per consolidation)
	SummaryTokens      int           // default 2500    (Codex summary token limit)
	MaxTranscriptBytes int           // default 8388608 (Codex MAX_ROLLOUT_BYTES)
	Lease              time.Duration // default 1h      (Codex JOB_LEASE_SECONDS)
	RetryBackoff       time.Duration // default 1h      (Codex JOB_RETRY_DELAY_SECONDS)
	AuxTimeout         time.Duration // default 10m
	MaxOutputBytes     int           // default 1048576 (auxiliary result cap)
	MaxNoteBytes       int           // default 8192
	MaxReadBytes       int           // default 65536
	MaxSearchResults   int           // default 50
}

// FundingMode is the operator-declared funding decision for this feature's
// background provider calls. The vocabulary is closed and carries no money
// shape: the host owns the funding machinery, and the mode never names an
// account.
type FundingMode string

const (
	// FundingModeUndeclared is the default: the operator declared no funding
	// decision, so the background work stays refused. The proxy never
	// silently enables a free provider call.
	FundingModeUndeclared FundingMode = ""
	// FundingModeCustomerFunded charges the background call to the account
	// the host's identity seam resolves and freezes at the trusted boundary.
	// It is admitted only on a host with a complete monetary binding.
	FundingModeCustomerFunded FundingMode = "customer_funded"
	// FundingModeOperatorSponsored runs the background call as usage-only:
	// the provider is opened, the usage is metered, and no customer money
	// moves.
	FundingModeOperatorSponsored FundingMode = "operator_sponsored"
)

// maxSponsorKeyBytes bounds one declared sponsor key. A sponsor key is a
// content-free token, never an account identifier or a principal.
const maxSponsorKeyBytes = 64

// ConfigError is a configuration rejection that names the offending field.
// It never echoes operator-supplied values.
type ConfigError struct {
	Field  string
	Reason string
}

func (e *ConfigError) Error() string {
	return ID + ": " + e.Field + ": " + e.Reason
}

// configFieldError builds one bounded rejection naming field.
func configFieldError(field, reason string) error {
	return &ConfigError{Field: field, Reason: reason}
}

// configFields is the complete snake_case key set of the feature subtree.
var configFields = []string{
	"principals", "allow_local_principal", "extract_route", "consolidate_route",
	"models",
	"funding_mode", "sponsor_keys", "sponsor_key",
	"sqlite_path",
	"min_idle", "recall_timeout", "max_session_age", "max_unused_age", "max_raw_memories",
	"poll_interval", "phase1_batch", "phase2_batch", "phase1_input_tokens",
	"phase2_input_tokens", "phase2_note_bytes", "summary_tokens",
	"max_transcript_bytes", "lease", "retry_backoff", "aux_timeout",
	"max_output_bytes", "max_note_bytes", "max_read_bytes", "max_search_results",
}

// The complete key sets of the model policy subtree (design §3). Every level
// is closed, so an unknown key in any level is rejected naming its own
// field.
var (
	modelPolicyFields    = []string{string(PhaseExtraction), string(PhaseConsolidation)}
	phasePolicyFields    = []string{"defaults", "locks", "allowed"}
	phaseTargetFields    = []string{FieldBackend, FieldModel, FieldReasoningEffort}
	phaseInventoryFields = []string{"backends", "models", "targets"}
	targetPolicyFields   = []string{FieldBackend, FieldModel, "reasoning_efforts"}
)

// modelPolicyStrictField is the operator's strict-policy switch: it refuses
// the facts the deployment does not prove instead of serving them explicitly
// degraded.
const modelPolicyStrictField = "strict"

// DefaultConfig returns the documented defaults for a config that sets only
// the required fields.
func DefaultConfig() Config {
	return Config{
		Models: ModelPolicy{
			Extraction: PhasePolicy{
				Defaults: PhaseTarget{ReasoningEffort: defaultPhaseEffort(PhaseExtraction)},
			},
			Consolidation: PhasePolicy{
				Defaults: PhaseTarget{ReasoningEffort: defaultPhaseEffort(PhaseConsolidation)},
			},
		},
		MinIdle:            defaultMinIdle,
		RecallTimeout:      defaultRecallTimeout,
		MaxSessionAge:      defaultMaxSessionAge,
		MaxUnusedAge:       defaultMaxUnusedAge,
		MaxRawMemories:     defaultMaxRawMemories,
		PollInterval:       defaultPollInterval,
		Phase1Batch:        defaultPhase1Batch,
		Phase2Batch:        defaultPhase2Batch,
		Phase1InputTokens:  defaultPhase1InputTokens,
		Phase2InputTokens:  defaultPhase2InputTokens,
		Phase2NoteBytes:    defaultPhase2NoteBytes,
		SummaryTokens:      defaultSummaryTokens,
		MaxTranscriptBytes: defaultMaxTranscriptBytes,
		Lease:              defaultLease,
		RetryBackoff:       defaultRetryBackoff,
		AuxTimeout:         defaultAuxTimeout,
		MaxOutputBytes:     defaultMaxOutputBytes,
		MaxNoteBytes:       defaultMaxNoteBytes,
		MaxReadBytes:       defaultMaxReadBytes,
		MaxSearchResults:   defaultMaxSearchResults,
	}
}

// DecodeConfig strictly decodes the feature config subtree: snake_case keys
// only, durations as strings, unknown keys rejected, and no nested enabled
// key. Enablement belongs to the outer plugins.features entry. The decoded
// configuration is validated before it is returned.
func DecodeConfig(n yaml.Node) (Config, error) {
	cfg := DefaultConfig()
	root, empty, err := configRoot(&n)
	if err != nil {
		return Config{}, err
	}
	if !empty {
		if err := decodeConfigMapping(root, &cfg); err != nil {
			return Config{}, err
		}
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// configRoot unwraps an optional document node and reports whether the subtree
// is empty (missing, null, or a document without content).
func configRoot(node *yaml.Node) (*yaml.Node, bool, error) {
	if node == nil || node.Kind == 0 {
		return nil, true, nil
	}
	if node.Kind == yaml.DocumentNode {
		if len(node.Content) == 0 {
			return nil, true, nil
		}
		if len(node.Content) != 1 {
			return nil, false, configFieldError("config", "invalid document")
		}
		node = node.Content[0]
	}
	if node.Kind == 0 || isNullNode(node) {
		return nil, true, nil
	}
	if node.Kind != yaml.MappingNode || node.Tag != "!!map" {
		return nil, false, configFieldError("config", "must be a mapping")
	}
	return node, false, nil
}

func isNullNode(node *yaml.Node) bool {
	return node != nil && node.Kind == yaml.ScalarNode && node.Tag == "!!null"
}

// decodeConfigMapping walks the mapping in order, rejecting invalid,
// unknown and duplicated keys, and fills cfg.
func decodeConfigMapping(root *yaml.Node, cfg *Config) error {
	return decodeMapping(root, "", configFields, func(name string, value *yaml.Node) error {
		return decodeConfigField(cfg, name, value)
	})
}

// decodeMapping walks a mapping node in order, rejecting invalid, unknown and
// duplicated keys, and calls decode for every accepted key. The path names the
// subtree in a rejection, so a nested subtree names its own field.
func decodeMapping(node *yaml.Node, path string, known []string, decode func(name string, value *yaml.Node) error) error {
	seen := make(map[string]struct{}, len(node.Content)/2)
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || len(key.Value) > 64 {
			return configFieldError(configPath(path), "contains an invalid key")
		}
		name := key.Value
		if !slices.Contains(known, name) {
			if path == "" && name == "enabled" {
				return configFieldError("enabled", "enablement belongs to the outer feature registration")
			}
			return configFieldError(configPath(path), "unknown key")
		}
		if _, duplicate := seen[name]; duplicate {
			return configFieldError(joinConfigPath(path, name), "duplicate key")
		}
		seen[name] = struct{}{}
		if err := decode(name, value); err != nil {
			return err
		}
	}
	return nil
}

// configPath names a configuration subtree in a rejection. The feature
// subtree itself is named "config".
func configPath(path string) string {
	if path == "" {
		return "config"
	}
	return path
}

// joinConfigPath names one key inside a configuration subtree.
func joinConfigPath(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

func decodeConfigField(cfg *Config, name string, value *yaml.Node) error {
	switch name {
	case "principals":
		principals, err := decodePrincipals(value)
		if err != nil {
			return err
		}
		cfg.Principals = principals
		return nil
	case "allow_local_principal":
		v, err := decodeBool(value, name)
		if err != nil {
			return err
		}
		cfg.AllowLocalPrincipal = v
		return nil
	case "extract_route":
		v, err := decodeScalarString(value, name)
		if err != nil {
			return err
		}
		cfg.ExtractRoute = strings.TrimSpace(v)
		return nil
	case "consolidate_route":
		v, err := decodeScalarString(value, name)
		if err != nil {
			return err
		}
		cfg.ConsolidateRoute = strings.TrimSpace(v)
		return nil
	case "models":
		return decodeModels(value, cfg)
	case "sqlite_path":
		v, err := decodeScalarString(value, name)
		if err != nil {
			return err
		}
		cfg.SQLitePath = strings.TrimSpace(v)
		return nil
	case "funding_mode":
		v, err := decodeScalarString(value, name)
		if err != nil {
			return err
		}
		cfg.FundingMode = FundingMode(strings.TrimSpace(v))
		return nil
	case "sponsor_keys":
		keys, err := decodeStringSequence(value, "sponsor_keys")
		if err != nil {
			return err
		}
		cfg.SponsorKeys = keys
		return nil
	case "sponsor_key":
		v, err := decodeScalarString(value, name)
		if err != nil {
			return err
		}
		cfg.SponsorKey = strings.TrimSpace(v)
		return nil
	}
	if target := durationTarget(cfg, name); target != nil {
		v, err := decodeDuration(value, name)
		if err != nil {
			return err
		}
		*target = v
		return nil
	}
	if target := intTarget(cfg, name); target != nil {
		v, err := decodeInt(value, name)
		if err != nil {
			return err
		}
		*target = v
		return nil
	}
	return configFieldError("config", "unknown key")
}

func durationTarget(cfg *Config, name string) *time.Duration {
	switch name {
	case "min_idle":
		return &cfg.MinIdle
	case "recall_timeout":
		return &cfg.RecallTimeout
	case "max_session_age":
		return &cfg.MaxSessionAge
	case "max_unused_age":
		return &cfg.MaxUnusedAge
	case "poll_interval":
		return &cfg.PollInterval
	case "lease":
		return &cfg.Lease
	case "retry_backoff":
		return &cfg.RetryBackoff
	case "aux_timeout":
		return &cfg.AuxTimeout
	}
	return nil
}

func intTarget(cfg *Config, name string) *int {
	switch name {
	case "max_raw_memories":
		return &cfg.MaxRawMemories
	case "phase1_batch":
		return &cfg.Phase1Batch
	case "phase2_batch":
		return &cfg.Phase2Batch
	case "phase1_input_tokens":
		return &cfg.Phase1InputTokens
	case "phase2_input_tokens":
		return &cfg.Phase2InputTokens
	case "phase2_note_bytes":
		return &cfg.Phase2NoteBytes
	case "summary_tokens":
		return &cfg.SummaryTokens
	case "max_transcript_bytes":
		return &cfg.MaxTranscriptBytes
	case "max_output_bytes":
		return &cfg.MaxOutputBytes
	case "max_note_bytes":
		return &cfg.MaxNoteBytes
	case "max_read_bytes":
		return &cfg.MaxReadBytes
	case "max_search_results":
		return &cfg.MaxSearchResults
	}
	return nil
}

func decodePrincipals(node *yaml.Node) ([]string, error) {
	return decodeStringSequence(node, "principals")
}

// decodeStringSequence decodes a sequence of trimmed, non-blank strings. The
// field name appears in the rejection so the operator sees which key is
// wrong, never the value.
func decodeStringSequence(node *yaml.Node, field string) ([]string, error) {
	if node.Kind != yaml.SequenceNode || node.Tag != "!!seq" {
		return nil, configFieldError(field, "must be a sequence of strings")
	}
	values := make([]string, 0, len(node.Content))
	for _, item := range node.Content {
		if item.Kind != yaml.ScalarNode || item.Tag != "!!str" {
			return nil, configFieldError(field, "must be a sequence of strings")
		}
		values = append(values, strings.TrimSpace(item.Value))
	}
	return values, nil
}

// decodeModels decodes the model policy subtree: the per-phase default
// target, the per-phase lock set and the per-phase allowed inventory of each
// phase (design §3).
//
// An operator that declares the subtree declares every phase, and every phase
// declares its default target, its lock set and its allowed inventory: a
// declaration that is missing is refused, while an individual default field
// that is missing keeps the operator's default route. A configuration without
// the subtree keeps the legacy behaviour unchanged, with no field locked.
func decodeModels(node *yaml.Node, cfg *Config) error {
	if node.Kind != yaml.MappingNode || node.Tag != "!!map" {
		return configFieldError("models", "must be a mapping")
	}
	cfg.ModelsDeclared = true
	seen := make(map[string]struct{}, len(modelPolicyFields))
	known := append(slices.Clone(modelPolicyFields), modelPolicyStrictField)
	err := decodeMapping(node, "models", known, func(name string, value *yaml.Node) error {
		seen[name] = struct{}{}
		switch name {
		case string(PhaseExtraction):
			return decodePhasePolicy(value, PhaseExtraction, &cfg.Models.Extraction)
		case string(PhaseConsolidation):
			return decodePhasePolicy(value, PhaseConsolidation, &cfg.Models.Consolidation)
		case modelPolicyStrictField:
			strict, err := decodeBool(value, joinConfigPath("models", modelPolicyStrictField))
			if err != nil {
				return err
			}
			cfg.Models.Strict = strict
			return nil
		}
		return configFieldError("models", "unknown key")
	})
	if err != nil {
		return err
	}
	for _, phase := range []Phase{PhaseExtraction, PhaseConsolidation} {
		if _, declared := seen[string(phase)]; !declared {
			return configFieldError(policyFieldPath(phase), "a declared model policy must declare every phase")
		}
	}
	return nil
}

// decodePhasePolicy decodes one phase's default target, lock set and
// allowed inventory.
func decodePhasePolicy(node *yaml.Node, phase Phase, policy *PhasePolicy) error {
	path := policyFieldPath(phase)
	if node.Kind != yaml.MappingNode || node.Tag != "!!map" {
		return configFieldError(path, "must be a mapping")
	}
	seen := make(map[string]struct{}, len(phasePolicyFields))
	err := decodeMapping(node, path, phasePolicyFields, func(name string, value *yaml.Node) error {
		seen[name] = struct{}{}
		switch name {
		case "defaults":
			return decodePhaseTarget(value, path+".defaults", &policy.Defaults)
		case "locks":
			return decodePhaseLocks(value, path+".locks", &policy.Locks)
		case "allowed":
			return decodePhaseInventory(value, path+".allowed", &policy.Inventory)
		}
		return configFieldError(path, "unknown key")
	})
	if err != nil {
		return err
	}
	for _, name := range phasePolicyFields {
		if _, declared := seen[name]; !declared {
			return configFieldError(path+"."+name, "a declared phase must declare its default target, its lock set and its allowed inventory")
		}
	}
	return nil
}

// decodePhaseLocks decodes one phase's lock set: the three user-facing fields
// the operator may hold. A field the operator does not name is not locked, so
// an empty lock set declares that the owner may choose every field the
// inventory permits (design §3; Requirement 1.2).
func decodePhaseLocks(node *yaml.Node, path string, locks *TargetLocks) error {
	if node.Kind != yaml.MappingNode || node.Tag != "!!map" {
		return configFieldError(path, "must be a mapping")
	}
	return decodeMapping(node, path, phaseTargetFields, func(name string, value *yaml.Node) error {
		locked, err := decodeBool(value, joinConfigPath(path, name))
		if err != nil {
			return err
		}
		switch name {
		case FieldBackend:
			locks.Backend = locked
		case FieldModel:
			locks.Model = locked
		case FieldReasoningEffort:
			locks.ReasoningEffort = locked
		}
		return nil
	})
}

// decodePhaseTarget decodes one phase's default target: the backend, the
// model and the reasoning effort.
func decodePhaseTarget(node *yaml.Node, path string, target *PhaseTarget) error {
	if node.Kind != yaml.MappingNode || node.Tag != "!!map" {
		return configFieldError(path, "must be a mapping")
	}
	return decodeMapping(node, path, phaseTargetFields, func(name string, value *yaml.Node) error {
		v, err := decodeScalarString(value, name)
		if err != nil {
			return configFieldError(joinConfigPath(path, name), "must be a string")
		}
		switch name {
		case FieldBackend:
			target.Backend = strings.TrimSpace(v)
		case FieldModel:
			target.Model = strings.TrimSpace(v)
		case FieldReasoningEffort:
			target.ReasoningEffort = strings.TrimSpace(v)
		}
		return nil
	})
}

// decodePhaseInventory decodes one phase's allowed inventory.
func decodePhaseInventory(node *yaml.Node, path string, inventory *PhaseInventory) error {
	if node.Kind != yaml.MappingNode || node.Tag != "!!map" {
		return configFieldError(path, "must be a mapping")
	}
	return decodeMapping(node, path, phaseInventoryFields, func(name string, value *yaml.Node) error {
		switch name {
		case "backends":
			values, ok := decodeStringList(value)
			if !ok {
				return configFieldError(path+"."+name, "must be a sequence of strings")
			}
			inventory.Backends = values
			return nil
		case "models":
			values, ok := decodeStringList(value)
			if !ok {
				return configFieldError(path+"."+name, "must be a sequence of strings")
			}
			inventory.Models = values
			return nil
		case "targets":
			targets, err := decodeTargetPolicies(value, path+"."+name)
			if err != nil {
				return err
			}
			inventory.Targets = targets
			return nil
		}
		return configFieldError(path, "unknown key")
	})
}

// decodeTargetPolicies decodes the per-target effort policy: each entry names
// the target it governs and the reasoning efforts a user may choose for it.
func decodeTargetPolicies(node *yaml.Node, path string) ([]TargetPolicy, error) {
	if node.Kind != yaml.SequenceNode || node.Tag != "!!seq" {
		return nil, configFieldError(path, "must be a sequence of target policies")
	}
	targets := make([]TargetPolicy, 0, len(node.Content))
	for _, item := range node.Content {
		if item.Kind != yaml.MappingNode || item.Tag != "!!map" {
			return nil, configFieldError(path, "must be a sequence of target policies")
		}
		var target TargetPolicy
		err := decodeMapping(item, path, targetPolicyFields, func(name string, value *yaml.Node) error {
			switch name {
			case FieldBackend:
				v, err := decodeScalarString(value, name)
				if err != nil {
					return configFieldError(path+"."+name, "must be a string")
				}
				target.Backend = strings.TrimSpace(v)
				return nil
			case FieldModel:
				v, err := decodeScalarString(value, name)
				if err != nil {
					return configFieldError(path+"."+name, "must be a string")
				}
				target.Model = strings.TrimSpace(v)
				return nil
			case "reasoning_efforts":
				values, ok := decodeStringList(value)
				if !ok {
					return configFieldError(path+"."+name, "must be a sequence of strings")
				}
				target.ReasoningEfforts = values
				return nil
			}
			return configFieldError(path, "unknown key")
		})
		if err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	return targets, nil
}

// decodeStringList decodes a sequence of trimmed strings.
func decodeStringList(node *yaml.Node) ([]string, bool) {
	if node.Kind != yaml.SequenceNode || node.Tag != "!!seq" {
		return nil, false
	}
	values := make([]string, 0, len(node.Content))
	for _, item := range node.Content {
		if item.Kind != yaml.ScalarNode || item.Tag != "!!str" {
			return nil, false
		}
		values = append(values, strings.TrimSpace(item.Value))
	}
	return values, true
}

func decodeBool(node *yaml.Node, field string) (bool, error) {
	if node.Kind != yaml.ScalarNode || node.Tag != "!!bool" {
		return false, configFieldError(field, "must be a boolean")
	}
	var v bool
	if err := node.Decode(&v); err != nil {
		return false, configFieldError(field, "must be a boolean")
	}
	return v, nil
}

func decodeScalarString(node *yaml.Node, field string) (string, error) {
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return "", configFieldError(field, "must be a string")
	}
	return node.Value, nil
}

func decodeDuration(node *yaml.Node, field string) (time.Duration, error) {
	value, err := decodeScalarString(node, field)
	if err != nil || len(value) > 64 {
		return 0, configFieldError(field, "must be a duration string such as 6h")
	}
	duration, parseErr := time.ParseDuration(value)
	if parseErr != nil {
		return 0, configFieldError(field, "must be a duration string such as 6h")
	}
	return duration, nil
}

func decodeInt(node *yaml.Node, field string) (int, error) {
	if node.Kind != yaml.ScalarNode || node.Tag != "!!int" {
		return 0, configFieldError(field, "must be an integer")
	}
	value, err := strconv.ParseInt(node.Value, 10, 32)
	if err != nil {
		return 0, configFieldError(field, "must be an integer")
	}
	return int(value), nil
}

// validateSponsorKey rejects a sponsor key that is not a bounded content-free
// token. A sponsor key names a sponsor, never an account: it must not carry
// an account identifier, a principal or any other user content.
func validateSponsorKey(key string) error {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" || trimmed != key || len(trimmed) > maxSponsorKeyBytes {
		return errors.New("proxy-memory: invalid sponsor key")
	}
	for _, r := range trimmed {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-' && r != '.' {
			return errors.New("proxy-memory: invalid sponsor key")
		}
	}
	return nil
}

// ephemeralSQLitePaths are the spellings of the in-memory SQLite database.
// The proxy-memory database is durable: an in-memory target would lose every
// decision, capture and memory without an operator-visible failure, so it is
// refused at the configuration boundary and at the isolated-database opener.
var ephemeralSQLitePaths = []string{":memory:", "file::memory:"}

// Validate checks the decoded or programmatically built configuration against
// the design §1 bounds and returns a *ConfigError naming the first offender.
func (c Config) Validate() error {
	if len(c.Principals) == 0 {
		return configFieldError("principals", "the allowlist must not be empty")
	}
	for _, principal := range c.Principals {
		trimmed := strings.TrimSpace(principal)
		if trimmed == "" {
			return configFieldError("principals", "allowlist entries must not be blank")
		}
		if trimmed == "*" && len(c.Principals) != 1 {
			return configFieldError("principals", `"*" must be the only allowlist entry`)
		}
	}
	if strings.TrimSpace(c.ExtractRoute) == "" {
		return configFieldError("extract_route", "an extraction route is required")
	}
	if strings.TrimSpace(c.ConsolidateRoute) == "" {
		return configFieldError("consolidate_route", "a consolidation route is required")
	}
	if err := c.validateModelPolicy(); err != nil {
		return err
	}
	switch c.FundingMode {
	case FundingModeUndeclared, FundingModeCustomerFunded, FundingModeOperatorSponsored:
	default:
		return configFieldError("funding_mode", "must be customer_funded or operator_sponsored")
	}
	for _, key := range c.SponsorKeys {
		if err := validateSponsorKey(key); err != nil {
			return configFieldError("sponsor_keys", "must be a bounded content-free token")
		}
	}
	if trimmed := strings.TrimSpace(c.SponsorKey); trimmed != "" {
		if err := validateSponsorKey(trimmed); err != nil {
			return configFieldError("sponsor_key", "must be a bounded content-free token")
		}
		if !slices.Contains(c.SponsorKeys, trimmed) {
			return configFieldError("sponsor_key", "must be one of the declared sponsor keys")
		}
	}
	if c.SQLitePath != "" && strings.ContainsAny(c.SQLitePath, "\x00?#&") {
		return configFieldError("sqlite_path", "must be a plain filesystem path")
	}
	for _, ephemeral := range ephemeralSQLitePaths {
		if strings.TrimSpace(c.SQLitePath) == ephemeral {
			// The memory database is durable: an in-memory target would lose
			// every decision and memory without an operator-visible failure.
			return configFieldError("sqlite_path", "must be a durable database file, not an in-memory database")
		}
	}
	durations := []struct {
		field string
		value time.Duration
	}{
		{"min_idle", c.MinIdle},
		{"recall_timeout", c.RecallTimeout},
		{"max_session_age", c.MaxSessionAge},
		{"max_unused_age", c.MaxUnusedAge},
		{"poll_interval", c.PollInterval},
		{"lease", c.Lease},
		{"retry_backoff", c.RetryBackoff},
		{"aux_timeout", c.AuxTimeout},
	}
	for _, d := range durations {
		if d.value <= 0 {
			return configFieldError(d.field, "must be a positive duration")
		}
	}
	if c.MinIdle >= c.MaxSessionAge {
		return configFieldError("min_idle", "must be less than max_session_age")
	}
	if c.AuxTimeout >= c.Lease {
		// One auxiliary job may run for aux_timeout while its lease is held for
		// lease; a timeout that is not strictly below the lease can outlive the
		// lease and let a second instance duplicate the work.
		return configFieldError("aux_timeout", "must be less than lease")
	}
	integers := []struct {
		field string
		value int
	}{
		{"max_raw_memories", c.MaxRawMemories},
		{"phase1_batch", c.Phase1Batch},
		{"phase2_batch", c.Phase2Batch},
		{"phase1_input_tokens", c.Phase1InputTokens},
		{"phase2_input_tokens", c.Phase2InputTokens},
		{"phase2_note_bytes", c.Phase2NoteBytes},
		{"summary_tokens", c.SummaryTokens},
		{"max_transcript_bytes", c.MaxTranscriptBytes},
		{"max_output_bytes", c.MaxOutputBytes},
		{"max_note_bytes", c.MaxNoteBytes},
		{"max_read_bytes", c.MaxReadBytes},
		{"max_search_results", c.MaxSearchResults},
	}
	for _, v := range integers {
		if v.value <= 0 {
			return configFieldError(v.field, "must be a positive integer")
		}
	}
	if c.Phase2NoteBytes < c.MaxNoteBytes {
		// Requirement 5.9 keeps notes that do not fit for a later consolidation.
		// The note budget must therefore admit at least one largest allowed note,
		// or a notes-only pass could never consume anything (design §7.3).
		return configFieldError("phase2_note_bytes", "must not be less than max_note_bytes")
	}
	if c.MaxReadBytes > 0 && c.MaxReadBytes < minReadBytesLimit {
		return configFieldError("max_read_bytes", "must be at least "+strconv.Itoa(minReadBytesLimit)+" to fit the truncation marker line")
	}
	if c.SummaryTokens > summaryTokensLimit {
		return configFieldError("summary_tokens", "must not exceed 4000")
	}
	if c.MaxTranscriptBytes > maxTranscriptBytesLimit {
		return configFieldError("max_transcript_bytes", "must not exceed 64 MiB")
	}
	return nil
}

// validateModelPolicy checks the per-phase model policy: every phase declares
// a bounded default target and a bounded allowed inventory, and a phase that
// names a backend or a model default needs a default route that names a
// single target (design §3).
func (c Config) validateModelPolicy() error {
	if err := c.Models.Extraction.validate(PhaseExtraction, "extract_route", c.ExtractRoute); err != nil {
		return err
	}
	if err := c.Models.Consolidation.validate(PhaseConsolidation, "consolidate_route", c.ConsolidateRoute); err != nil {
		return err
	}
	if !c.ModelsDeclared {
		return nil
	}
	// A declared policy is enforced per attempt, and the core adds no
	// coordination across the arms of a parallel round or the alternatives
	// of a failover route. A governed phase therefore runs on a single
	// target, so a refusal ends the phase instead of serving another
	// candidate (design §2).
	if err := requireSingleTargetRoute("extract_route", c.ExtractRoute); err != nil {
		return err
	}
	return requireSingleTargetRoute("consolidate_route", c.ConsolidateRoute)
}

// requireSingleTargetRoute refuses a phase route that names more than one
// target.
func requireSingleTargetRoute(field, route string) error {
	shape, err := parseRouteShape(route)
	if err != nil {
		return configFieldError(field, "must be a valid route selector")
	}
	if shape.multiTarget {
		return configFieldError(field, "must name a single target when the model policy is declared")
	}
	return nil
}
