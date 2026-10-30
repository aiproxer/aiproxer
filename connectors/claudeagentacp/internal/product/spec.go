package product

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/connector-support/acp"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

// ClaudeSpec supplies immutable Claude policy to the existing ACP runtime.
// Construction does not certify or launch a runtime: the assembly must supply a
// certified executable and AuthEnv-decorated starter after the policy gates.
type ClaudeSpec struct {
	executable string
	workspace  string
	auth       *AuthEnv
	models     atomic.Pointer[modelSnapshot]
	// gradedStale marks the published graded-effort evidence as no longer
	// current after a failed discovery refresh. Inventory rows stay usable,
	// but graded facts answer Unknown until a valid refresh republishes them.
	// Publication and invalidation both happen under discovery admission, so
	// admission order alone decides which of them is the last word.
	gradedStale atomic.Bool
}

// ModelDiscovery is the bounded private _aip/models_v1 response. The caller
// owns discovery process lifetime and supplies the actual successful fetch time.
type ModelDiscovery struct {
	Version        int               `json:"version"`
	CurrentModelID string            `json:"currentModelId"`
	Models         []DiscoveredModel `json:"models"`
}

type DiscoveredModel struct {
	NativeID     string                     `json:"nativeId"`
	DisplayName  string                     `json:"displayName"`
	Description  string                     `json:"description"`
	Capabilities map[string]json.RawMessage `json:"capabilities"`
}

type modelSnapshot struct {
	inventory modelinventory.Snapshot
	current   string
	rows      []DiscoveredModel
}

var (
	exactNativeID = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_.-]*[0-9][a-zA-Z0-9_.-]*(\[[0-9]+[mk]\])?$`)
	shortVersion  = regexp.MustCompile(`^([a-zA-Z][a-zA-Z-]*)-([0-9]{1,2})-([0-9]{1,2})((-[0-9]{8})?(-[0-9]+[mk]|\[[0-9]+[mk]\])?)$`)
)

func canonicalModelID(native string) string {
	return "anthropic/" + shortVersion.ReplaceAllString(native, "${1}-${2}.${3}${4}")
}

func validNativeID(id string) bool { return len(id) <= 256 && exactNativeID.MatchString(id) }

func validModelText(value string) bool {
	return len(value) <= 2048 && utf8.ValidString(value) && !strings.ContainsFunc(value, unicode.IsControl)
}

// PublishModels validates the entire response before one atomic replacement.
// It never turns a failed/empty discovery into static defaults or partial state.
func (s *ClaudeSpec) PublishModels(discovery ModelDiscovery, fetchedAt time.Time) error {
	invalid := func() error {
		return fmt.Errorf("%w: claudeagentacp invalid discovered model snapshot", aipapi.ErrCapabilityReject)
	}
	if discovery.Version != 1 || len(discovery.Models) == 0 || len(discovery.Models) > 256 || fetchedAt.IsZero() {
		return invalid()
	}
	next := &modelSnapshot{current: discovery.CurrentModelID, inventory: modelinventory.Snapshot{Source: modelinventory.SourceRemote, LoadedAt: fetchedAt, Warnings: []string{}}}
	seen := make(map[string]bool, len(discovery.Models))
	current := false
	for _, row := range discovery.Models {
		canonical := canonicalModelID(row.NativeID)
		if !validNativeID(row.NativeID) || seen[canonical] || !validModelText(row.DisplayName) || !validModelText(row.Description) {
			return invalid()
		}
		seen[canonical] = true
		current = current || row.NativeID == next.current
		copied := row
		copied.Capabilities = make(map[string]json.RawMessage, len(row.Capabilities))
		size := 0
		for name, value := range row.Capabilities {
			size += len(name) + len(value)
			if !json.Valid(value) || size > 16384 || len(name) > 256 || !validModelText(name) {
				return invalid()
			}
			if strings.HasPrefix(name, "supports") {
				var flag *bool
				if json.Unmarshal(value, &flag) != nil || flag == nil {
					return invalid()
				}
			}
			if name == "supportedEffortLevels" {
				var levels []string
				if json.Unmarshal(value, &levels) != nil || levels == nil {
					return invalid()
				}
				seenLevels := make(map[string]bool, len(levels))
				for _, level := range levels {
					if level == "" || !validModelText(level) || seenLevels[level] {
						return invalid()
					}
					seenLevels[level] = true
				}
			}
			copied.Capabilities[name] = slices.Clone(value)
		}
		next.rows = append(next.rows, copied)
		// The connector knows which model is current, so it designates it as
		// the instance's default model. The host derives its default route from
		// that designation instead of guessing from the enumeration order.
		next.inventory.Models = append(next.inventory.Models, modelinventory.Model{
			NativeID:    row.NativeID,
			CanonicalID: canonical,
			DisplayName: row.DisplayName,
			IsDefault:   row.NativeID == next.current,
		})
	}
	if !current {
		return invalid()
	}
	s.models.Store(next)
	s.gradedStale.Store(false)
	return nil
}

// InvalidateGradedEffortEvidence marks the current graded-effort evidence as
// stale after a failed discovery refresh. Inventory publication is untouched:
// the connector keeps serving the last accepted inventory, while effort facts
// answer Unknown until a valid refresh republishes them.
func (s *ClaudeSpec) InvalidateGradedEffortEvidence() {
	if s == nil {
		return
	}
	s.gradedStale.Store(true)
}

var (
	_ acp.SubprocessConnectorSpec = (*ClaudeSpec)(nil)
	_ acp.SessionOptionsSpec      = (*ClaudeSpec)(nil)
	_ acp.TransportOptionsSpec    = (*ClaudeSpec)(nil)
	_ acp.RuntimeIdentitySpec     = (*ClaudeSpec)(nil)
)

// NewClaudeSpec authorizes no models until a validated discovery is published.
func NewClaudeSpec(executable, workspace string, auth *AuthEnv) (*ClaudeSpec, error) {
	if strings.TrimSpace(executable) == "" || auth == nil {
		return nil, errors.New("claudeagentacp: resolved executable and authentication profile required")
	}
	return &ClaudeSpec{executable: executable, workspace: workspace, auth: auth}, nil
}

// Inventory borrows the atomically published discovery; session advertisements
// remain independently authoritative at the turn boundary.
func (s *ClaudeSpec) Inventory() modelinventory.Provider { return discoveredInventory{s} }

type discoveredInventory struct{ spec *ClaudeSpec }

func (p discoveredInventory) LoadModels(ctx context.Context) (modelinventory.Snapshot, error) {
	if ctx == nil {
		return modelinventory.Snapshot{}, modelinventory.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return modelinventory.Snapshot{}, err
	}
	state := p.spec.models.Load()
	if state == nil {
		return modelinventory.Snapshot{}, errors.New("claudeagentacp: runtime model discovery required")
	}
	snapshot := state.inventory
	snapshot.Models = slices.Clone(snapshot.Models)
	snapshot.Warnings = slices.Clone(snapshot.Warnings)
	return snapshot, nil
}

func (*ClaudeSpec) VendorID() string                { return "claudeagentacp" }
func (*ClaudeSpec) VendorPrefix() string            { return "anthropic" }
func (*ClaudeSpec) RequiresExplicitWorkspace() bool { return true }
func (*ClaudeSpec) EffortConfigID() string          { return "effort" }

// RuntimeIdentity scopes the runtime pool to one conversation. The Claude
// agent process carries the conversation context, so two conversations must
// never share one process: the shared key would hand the second conversation
// the first conversation's turns.
func (s *ClaudeSpec) RuntimeIdentity(call *aipapi.Call) string {
	return acp.CallRuntimeIdentity(call)
}

func (s *ClaudeSpec) ResolveModel(model string) string {
	if s == nil {
		return ""
	}
	return resolveNativeInSnapshot(s.models.Load(), model)
}

// resolveNativeInSnapshot resolves one model against a single immutable
// snapshot. Callers that already hold a snapshot must use it directly instead
// of reloading, so identity and capability reads observe the same publication.
func resolveNativeInSnapshot(state *modelSnapshot, model string) string {
	if state == nil {
		return ""
	}
	if model == "" || model == "auto" {
		return state.current
	}
	for _, row := range state.inventory.Models {
		if model == row.CanonicalID || model == row.NativeID {
			return row.NativeID
		}
	}
	return ""
}

// GradedEffortLevels returns the provider-verified graded effort values for
// one native model, read from the latest accepted discovery snapshot.
//
// It is read-only and exact: canonical and native identities map through
// the same immutable snapshot, never fuzzy matching and never two snapshot
// loads. The second result is false when discovery has not run, the model
// is unproven, or the snapshot carries no graded fact for the model.
// A binary supportsEffort flag alone never yields a graded claim.
// Completeness of the upstream supportedEffortLevels list is not proven, so
// callers must advertise the members with exhaustive=false.
func (s *ClaudeSpec) GradedEffortLevels(model string) ([]string, bool) {
	if model == "" || model == "auto" {
		return nil, false
	}
	if s == nil || s.gradedStale.Load() {
		return nil, false
	}
	state := s.models.Load()
	if state == nil {
		return nil, false
	}
	native := resolveNativeInSnapshot(state, model)
	if native == "" {
		return nil, false
	}
	for _, row := range state.rows {
		if row.NativeID != native {
			continue
		}
		// An explicit non-positive binary effort declaration contradicts a
		// graded list: never publish the contradiction as verified support.
		if flag, ok := row.Capabilities["supportsEffort"]; ok {
			var supported *bool
			if json.Unmarshal(flag, &supported) != nil || supported == nil || !*supported {
				return nil, false
			}
		}
		raw, ok := row.Capabilities["supportedEffortLevels"]
		if !ok {
			return nil, false
		}
		var levels []string
		if err := json.Unmarshal(raw, &levels); err != nil || len(levels) == 0 {
			return nil, false
		}
		seen := make(map[string]struct{}, len(levels))
		for _, level := range levels {
			if level == "" || !validModelText(level) {
				return nil, false
			}
			if _, dup := seen[level]; dup {
				return nil, false
			}
			seen[level] = struct{}{}
		}
		return slices.Clone(levels), true
	}
	return nil, false
}

// BuildCommand enforces the inventory at the last boundary before spawn. Model
// selection belongs exclusively to session options, never command-line flags.
func (s *ClaudeSpec) BuildCommand(model, workspace string) ([]string, string, []string, error) {
	state := s.models.Load()
	known := state != nil && slices.ContainsFunc(state.inventory.Models, func(m modelinventory.Model) bool { return m.NativeID == model })
	if !known {
		return nil, "", nil, fmt.Errorf("%w: claudeagentacp model is not in instance inventory", aipapi.ErrCapabilityReject)
	}
	return []string{s.executable}, workspace, nil, nil
}

func (*ClaudeSpec) InitialSessionOptions(model string) map[string]string {
	return map[string]string{"model": model}
}

func (s *ClaudeSpec) HandshakeProfile() acp.HandshakeProfile {
	profile := s.auth.HandshakeProfile()
	profile.SessionNewCwd = s.workspace
	return profile
}

func (*ClaudeSpec) CancelProfile() acp.CancelProfile {
	return acp.CancelProfile{Methods: []string{"session/cancel"}}
}

func (s *ClaudeSpec) ServerRequestHandler() acp.ServerRequestHandler {
	return s.auth.ServerRequestHandler()
}

func (s *ClaudeSpec) TransportOptions() acp.TransportOptions {
	return s.auth.TransportOptions()
}

// ClassifyError supplies source-specific, safe failures to the engine assembly.
// The caller owns output commitment and must pass the actual output phase.
func (s *ClaudeSpec) ClassifyError(err error, phase aipapi.OutputPhase) error {
	return classifyError(err, phase, s.auth.source)
}
