package backendplugin

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Ordered Configure secret-slot budgets. They mirror the host capture
// budgets (hostcredentials and runtimebundle provisioning) so connector-side
// validation can never accept a bundle the host could not have emitted, and
// no protobuf/Configure RPC change is needed to carry ordered credentials.
const (
	// MaxCredentialSlotCount bounds the number of ordered slots in one bundle.
	MaxCredentialSlotCount = 256
	// MaxCredentialSlotSecretBytes bounds one slot value.
	MaxCredentialSlotSecretBytes = 64 << 10
	// MaxCredentialSlotTotalBytes bounds the complete ordered set.
	MaxCredentialSlotTotalBytes = 256 << 10
)

// OrderedSecretSlot is one validated Configure secret in canonical order:
// the bare base name is slot 1, base_N fills ascending numbered slots.
// Value is an owned copy the caller may retain. Name and Index are nonsecret
// and safe for diagnostics; Value is secret material and must never be
// logged.
type OrderedSecretSlot struct {
	Name  string
	Index int
	Value []byte
}

// DecodeOrderedSecretSlots validates the canonical ordered Configure slots
// for base (api_key or api_token) and returns owned copies in ascending slot
// order: api_key, api_key_2, ... Sparse indexes are preserved (gaps stay
// gaps); map iteration order is never meaningful. Every rejection is
// fail-closed and secret-free: errors name only slot names and the base,
// never secret bytes. Values are compared for uniqueness but never echoed.
func DecodeOrderedSecretSlots(bundle SecretBundle, base CredentialSlot) ([]OrderedSecretSlot, error) {
	if base != CredentialSlotAPIKey && base != CredentialSlotAPIToken {
		return nil, fmt.Errorf("%w: unknown slot base %q", ErrInvalidCredentialSlots, string(base))
	}
	if len(bundle.Values) == 0 {
		return nil, fmt.Errorf("%w: missing %q slot", ErrInvalidCredentialSlots, string(base))
	}
	if len(bundle.Values) > MaxCredentialSlotCount {
		return nil, fmt.Errorf("%w: %d slots exceed %d", ErrInvalidCredentialSlots, len(bundle.Values), MaxCredentialSlotCount)
	}
	prefix := string(base)
	names := make([]string, 0, len(bundle.Values))
	for name := range bundle.Values {
		names = append(names, name)
	}
	sort.Strings(names)
	slots := make([]OrderedSecretSlot, 0, len(names))
	seenValues := make(map[string]string, len(names))
	total := 0
	for _, name := range names {
		index, ok := parseOrderedSlotIndex(prefix, name)
		if !ok {
			return nil, fmt.Errorf("%w: unexpected secret name %q for slot %q", ErrInvalidCredentialSlots, name, prefix)
		}
		value := bundle.Values[name]
		if len(value) == 0 {
			return nil, fmt.Errorf("%w: empty value for slot %q", ErrInvalidCredentialSlots, name)
		}
		if bytes.IndexByte(value, 0) >= 0 {
			return nil, fmt.Errorf("%w: invalid secret bytes for slot %q", ErrInvalidCredentialSlots, name)
		}
		if len(value) > MaxCredentialSlotSecretBytes {
			return nil, fmt.Errorf("%w: value for slot %q exceeds %d bytes", ErrInvalidCredentialSlots, name, MaxCredentialSlotSecretBytes)
		}
		if first, dup := seenValues[string(value)]; dup {
			return nil, fmt.Errorf("%w: duplicate value in slots %q and %q", ErrInvalidCredentialSlots, first, name)
		}
		seenValues[string(value)] = name
		total += len(value)
		if total > MaxCredentialSlotTotalBytes {
			return nil, fmt.Errorf("%w: slots for %q exceed %d total bytes", ErrInvalidCredentialSlots, prefix, MaxCredentialSlotTotalBytes)
		}
		slots = append(slots, OrderedSecretSlot{Name: name, Index: index, Value: bytes.Clone(value)})
	}
	sort.Slice(slots, func(i, j int) bool {
		if slots[i].Index != slots[j].Index {
			return slots[i].Index < slots[j].Index
		}
		return slots[i].Name < slots[j].Name
	})
	return slots, nil
}

// parseOrderedSlotIndex accepts the bare base (slot 1) and canonical
// base_N suffixes with 2 <= N <= MaxCredentialSlotCount. The suffix must be
// the canonical decimal form (no padding): _0, _1, non-numeric and
// out-of-range suffixes are rejected, matching the host resolver that never
// emits them.
func parseOrderedSlotIndex(prefix, name string) (int, bool) {
	if name == prefix {
		return 1, true
	}
	rest, ok := strings.CutPrefix(name, prefix+"_")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n < 2 || n > MaxCredentialSlotCount {
		return 0, false
	}
	if rest != strconv.Itoa(n) {
		return 0, false
	}
	return n, true
}
