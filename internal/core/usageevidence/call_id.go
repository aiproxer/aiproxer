package usageevidence

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const (
	billingCallIDPrefix = "bc_"
	billingCallIDHexLen = 32
)

var ErrBillingCallIDInvalid = errors.New("billing: invalid billing call id")

type BillingCallID string

func NewBillingCallID() (BillingCallID, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("billing: billing call id entropy: %w", err)
	}
	id := BillingCallID(billingCallIDPrefix + hex.EncodeToString(raw[:]))
	if err := id.Validate(); err != nil {
		return "", err
	}
	return id, nil
}

func ParseBillingCallID(raw string) (BillingCallID, error) {
	id := BillingCallID(strings.TrimSpace(raw))
	if err := id.Validate(); err != nil {
		return "", err
	}
	return id, nil
}
func (id BillingCallID) String() string { return string(id) }
func (id BillingCallID) Validate() error {
	raw := string(id)
	if strings.TrimSpace(raw) != raw || raw == "" {
		return fmt.Errorf("%w: value is required", ErrBillingCallIDInvalid)
	}
	if !strings.HasPrefix(raw, billingCallIDPrefix) {
		return fmt.Errorf("%w: missing %s prefix", ErrBillingCallIDInvalid, billingCallIDPrefix)
	}
	payload := raw[len(billingCallIDPrefix):]
	if len(payload) != billingCallIDHexLen {
		return fmt.Errorf("%w: unexpected length", ErrBillingCallIDInvalid)
	}
	if _, err := hex.DecodeString(payload); err != nil {
		return fmt.Errorf("%w: %v", ErrBillingCallIDInvalid, err)
	}
	return nil
}
