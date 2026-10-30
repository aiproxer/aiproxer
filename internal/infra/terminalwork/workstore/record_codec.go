package workstore

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/terminalwork"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

type workRow struct {
	StoreID             string `bun:"store_id,pk"`
	WorkID              string `bun:"work_id,pk"`
	SourceKey           string `bun:"source_key"`
	IdentityVersion     int    `bun:"identity_version"`
	PayloadVersion      int    `bun:"payload_version"`
	Kind                string `bun:"kind"`
	State               string `bun:"state"`
	ProviderID          string `bun:"provider_id"`
	RequestID           string `bun:"request_id"`
	AttemptID           string `bun:"attempt_id"`
	TraceID             string `bun:"trace_id"`
	GenerationID        string `bun:"generation_id"`
	RuntimeInstanceID   string `bun:"runtime_instance_id"`
	RuntimeGenerationID string `bun:"runtime_generation_id"`
	BoundProviderID     string `bun:"bound_provider_id"`
	RatingID            string `bun:"rating_id"`
	FactID              string `bun:"fact_id"`
	LeaseSetID          string `bun:"lease_set_id"`
	PayloadJSON         string `bun:"payload_json"`
	Attempts            int    `bun:"attempts"`
	NextRetryAtUnix     int64  `bun:"next_retry_at_unix"`
	ClaimOwnerID        string `bun:"claim_owner_id"`
	ClaimExpiresAtUnix  int64  `bun:"claim_expires_at_unix"`
	ErrorCode           string `bun:"error_code"`
	ErrorPermanent      bool   `bun:"error_permanent"`
	ErrorMessage        string `bun:"error_message"`
	CreatedAtUnix       int64  `bun:"created_at_unix"`
	UpdatedAtUnix       int64  `bun:"updated_at_unix"`
}

func (*workRow) TableName() string { return "economic_terminal_work" }

func recordToRow(storeID string, rec terminalwork.WorkRecord) workRow {
	payload := rec.Payload
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	return workRow{
		StoreID:             storeID,
		WorkID:              rec.WorkID,
		SourceKey:           rec.SourceKey.String(),
		IdentityVersion:     rec.SourceKey.IdentityVersion,
		PayloadVersion:      rec.PayloadVersion,
		Kind:                string(rec.Kind),
		State:               string(rec.State),
		ProviderID:          rec.ProviderID,
		RequestID:           rec.Lifecycle.RequestID,
		AttemptID:           rec.Lifecycle.AttemptID,
		TraceID:             rec.Lifecycle.TraceID,
		GenerationID:        rec.Versions.GenerationID,
		RuntimeInstanceID:   rec.Versions.RuntimeInstanceID,
		RuntimeGenerationID: rec.Versions.RuntimeGenerationID,
		BoundProviderID:     rec.Versions.ProviderID,
		RatingID:            rec.Versions.RatingID,
		FactID:              rec.FactID,
		LeaseSetID:          rec.LeaseSetID,
		PayloadJSON:         string(payload),
		Attempts:            rec.Attempts,
		NextRetryAtUnix:     timeUnixNano(rec.NextRetryAt),
		ClaimOwnerID:        rec.Lease.OwnerID,
		ClaimExpiresAtUnix:  timeUnixNano(rec.Lease.ExpiresAt),
		ErrorCode:           rec.Error.Code,
		ErrorPermanent:      rec.Error.Permanent,
		ErrorMessage:        rec.Error.Message,
		CreatedAtUnix:       timeUnixNano(rec.CreatedAt),
		UpdatedAtUnix:       timeUnixNano(rec.UpdatedAt),
	}
}

func rowToRecord(row workRow) (terminalwork.WorkRecord, error) {
	sk := parseSourceKey(row.SourceKey, row.IdentityVersion)
	rec := terminalwork.WorkRecord{
		WorkID:         row.WorkID,
		SourceKey:      sk,
		PayloadVersion: row.PayloadVersion,
		Kind:           sdk.WorkKind(row.Kind),
		State:          sdk.WorkState(row.State),
		ProviderID:     row.ProviderID,
		Lifecycle: terminalwork.LifecycleCorrelation{
			RequestID: row.RequestID,
			AttemptID: row.AttemptID,
			TraceID:   row.TraceID,
		},
		Versions: terminalwork.BoundVersions{
			GenerationID:        row.GenerationID,
			RuntimeInstanceID:   row.RuntimeInstanceID,
			RuntimeGenerationID: row.RuntimeGenerationID,
			ProviderID:          row.BoundProviderID,
			RatingID:            row.RatingID,
		},
		Payload:     []byte(row.PayloadJSON),
		FactID:      row.FactID,
		LeaseSetID:  row.LeaseSetID,
		Attempts:    row.Attempts,
		NextRetryAt: timeFromUnixNano(row.NextRetryAtUnix),
		Lease: terminalwork.ClaimLease{
			OwnerID:   row.ClaimOwnerID,
			ExpiresAt: timeFromUnixNano(row.ClaimExpiresAtUnix),
		},
		Error: terminalwork.BoundedError{
			Code:      row.ErrorCode,
			Permanent: row.ErrorPermanent,
			Message:   row.ErrorMessage,
		},
		CreatedAt: timeFromUnixNano(row.CreatedAtUnix),
		UpdatedAt: timeFromUnixNano(row.UpdatedAtUnix),
	}
	if err := rec.Validate(); err != nil {
		return terminalwork.WorkRecord{}, fmt.Errorf("decode record: %w", err)
	}
	return rec, nil
}

func parseSourceKey(encoded string, identityVersion int) terminalwork.SourceKey {
	if strings.HasPrefix(encoded, "v") {
		if idx := strings.IndexByte(encoded, ':'); idx > 1 {
			return terminalwork.SourceKey{IdentityVersion: identityVersion, Key: encoded[idx+1:]}
		}
	}
	return terminalwork.SourceKey{IdentityVersion: identityVersion, Key: encoded}
}

func timeUnixNano(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UTC().UnixNano()
}

func timeFromUnixNano(ns int64) time.Time {
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns).UTC()
}

func payloadDigest(payload []byte) string {
	if len(payload) == 0 {
		return "{}"
	}
	var v any
	if err := json.Unmarshal(payload, &v); err != nil {
		return string(payload)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return string(payload)
	}
	return string(b)
}
