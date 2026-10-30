// Package hostcredentials owns the single policy-driven numbered
// environment-key resolver for static API-key credentials.
//
// Both the standard-distribution host capture (internal/standardplugins) and
// the compatible-family helpers (internal/plugins/backends/compatmode)
// resolve through here, so sparse numbering, slot de-duplication, root_1
// rejection, value-size budgets and secret-free errors cannot diverge between
// the two historical scanners this package replaces.
//
// Exact discovery: the bare root fills slot 1 and root_2..root_N are scanned
// in ascending numeric order without stopping at gaps. A set root_1 variable
// is invalid/misconfigured, never a second slot. Values are trimmed; empty
// slots are skipped; values containing NUL bytes or exceeding the per-value
// budget are rejected; the total captured bytes are bounded. Equal values
// deduplicate to their first occurrence, keeping numeric slot identity as
// nonsecret metadata. Errors name environment roots and slot indexes only and
// never carry secret values.
package hostcredentials

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

const (
	// MaxValueBytes bounds one captured secret value. It mirrors the
	// executable-connector secret capture budget so host and connector lanes
	// enforce the same ceiling.
	MaxValueBytes = 64 << 10
	// MaxTotalBytes bounds the complete captured set for one resolution call.
	MaxTotalBytes = 256 << 10
)

var (
	// ErrInvalidEnvRoot reports a malformed bare credential namespace.
	ErrInvalidEnvRoot = errors.New("hostcredentials: invalid credential environment root")
	// ErrRoot1Misconfigured reports a set <root>_1 variable, which is never a
	// valid slot under the bare + _2.._N convention.
	ErrRoot1Misconfigured = errors.New("hostcredentials: misconfigured _1 suffixed variable")
	// ErrSlotValueInvalid reports a slot value with invalid secret bytes.
	ErrSlotValueInvalid = errors.New("hostcredentials: invalid credential value")
	// ErrSlotValueTooLarge reports a slot value over the per-value budget.
	ErrSlotValueTooLarge = errors.New("hostcredentials: credential value exceeds size budget")
	// ErrTotalTooLarge reports a captured set over the total budget.
	ErrTotalTooLarge = errors.New("hostcredentials: captured credentials exceed total size budget")
	// ErrMaxIndexOutOfRange reports a scan bound outside 1..256.
	ErrMaxIndexOutOfRange = errors.New("hostcredentials: max_env_index out of range")
	// ErrDuplicateRoot reports one root listed twice in a resolution set.
	ErrDuplicateRoot = errors.New("hostcredentials: duplicate credential environment root")
)

// Environment is the minimal host-lookup capability the resolver needs. Both
// featurehost.HostEnvironment and osenv.Process satisfy it structurally, so
// neither needs importing here.
type Environment interface {
	Lookup(name string) (value string, ok bool)
}

// Slot is one resolved numbered credential: Index 1 is the bare root,
// indexes 2..N are the ascending numeric suffixes. Value is secret material:
// never log it or include it in error text.
type Slot struct {
	Index int
	Value string
}

// Values projects ordered secret values out of resolved slots.
func Values(slots []Slot) []string {
	out := make([]string, 0, len(slots))
	for _, slot := range slots {
		out = append(out, slot.Value)
	}
	return out
}

// slotName returns the environment variable name for slot index: the bare
// root for 1, root_<i> otherwise.
func slotName(root string, index int) string {
	if index <= 1 {
		return root
	}
	return root + "_" + strconv.Itoa(index)
}

// Resolve deterministically scans root (slot 1) and root_2..root_maxIndex
// through env without stopping at gaps. An empty root resolves no-auth with
// no lookup. maxIndex must be within
// config.MinUpstreamMaxEnvIndex..config.MaxUpstreamMaxEnvIndex.
func Resolve(root string, env Environment, maxIndex int) ([]Slot, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, nil
	}
	if err := config.ValidateCredentialEnvRoot(root); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidEnvRoot, err)
	}
	if maxIndex < config.MinUpstreamMaxEnvIndex || maxIndex > config.MaxUpstreamMaxEnvIndex {
		return nil, fmt.Errorf("%w: got %d, want %d..%d",
			ErrMaxIndexOutOfRange, maxIndex, config.MinUpstreamMaxEnvIndex, config.MaxUpstreamMaxEnvIndex)
	}
	if env == nil {
		return nil, nil
	}
	if name := root + "_1"; envHasValue(env, name) {
		return nil, fmt.Errorf("%w: %q is not a valid slot (bare name is slot 1, numbered suffixes start at _2)", ErrRoot1Misconfigured, name)
	}
	slots := make([]Slot, 0, maxIndex)
	seen := make(map[string]struct{})
	total := 0
	for index := 1; index <= maxIndex; index++ {
		raw, ok := env.Lookup(slotName(root, index))
		if !ok {
			continue
		}
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		if strings.ContainsRune(value, 0) {
			return nil, fmt.Errorf("%w: %q holds invalid secret bytes", ErrSlotValueInvalid, slotName(root, index))
		}
		if len(value) > MaxValueBytes {
			return nil, fmt.Errorf("%w: %q exceeds %d bytes", ErrSlotValueTooLarge, slotName(root, index), MaxValueBytes)
		}
		if _, dup := seen[value]; dup {
			continue
		}
		seen[value] = struct{}{}
		total += len(value)
		if total > MaxTotalBytes {
			return nil, fmt.Errorf("%w: root %q exceeds %d bytes", ErrTotalTooLarge, root, MaxTotalBytes)
		}
		slots = append(slots, Slot{Index: index, Value: value})
	}
	return slots, nil
}

// ResolveAll resolves every non-empty root in order and reports the slots key
// by bare root. A root listed twice is an accidental namespace collision, not
// sharing: it fails closed naming only the colliding root.
func ResolveAll(roots []string, env Environment, maxIndex int) (map[string][]Slot, error) {
	out := make(map[string][]Slot, len(roots))
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		if _, dup := out[root]; dup {
			return nil, fmt.Errorf("%w: %q", ErrDuplicateRoot, root)
		}
		slots, err := Resolve(root, env, maxIndex)
		if err != nil {
			return nil, err
		}
		out[root] = slots
	}
	return out, nil
}

// envHasValue reports whether name resolves to a non-blank value.
func envHasValue(env Environment, name string) bool {
	raw, ok := env.Lookup(name)
	return ok && strings.TrimSpace(raw) != ""
}
