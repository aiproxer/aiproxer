package oauthcred

import (
	"time"
)

// TokenRecord holds OAuth credential information stored on disk.
type TokenRecord struct {
	AccessToken      string    `json:"access_token"`
	RefreshToken     string    `json:"refresh_token,omitempty"`
	Expiry           time.Time `json:"expiry"`
	Quarantined      bool      `json:"quarantined,omitempty"`
	QuarantineReason string    `json:"quarantine_reason,omitempty"`
}

// IsExpired returns true if the access token has expired or will expire within the given skew duration.
func (r TokenRecord) IsExpired(skew time.Duration) bool {
	if r.AccessToken == "" {
		return true
	}
	if r.Expiry.IsZero() {
		return false
	}
	return time.Until(r.Expiry) <= skew
}
