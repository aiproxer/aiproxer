package codex

import (
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	aipsdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// AccountWindowSnapshot is the connector-local, non-debit representation of a
// Codex allowance response header set. It deliberately has no B-leg identity:
// a gauge observed while a response is in flight cannot be treated as that
// request's debit.
type AccountWindowSnapshot struct {
	ProviderAccountKey string
	PoolID             string
	WindowID           string
	ResetAt            time.Time
	ObservedAt         time.Time
	UsedPercent        *aipsdkmetering.Decimal
	RemainingPercent   *aipsdkmetering.Decimal
	Limit              *aipsdkmetering.Decimal
	Credits            *aipsdkmetering.Decimal
	Semantics          string
	Origin             string
	Acquisition        string
	Authority          string
}

// AccountWindowSnapshotSource is intentionally separate from the B-leg
// ObservationSource. A host that owns provider-account persistence may promote
// these snapshots to SubjectAccountWindow; no request path may do so itself.
type AccountWindowSnapshotSource interface {
	DrainAccountWindowSnapshots() []AccountWindowSnapshot
}

var codexWindowHeaderPattern = regexp.MustCompile(`^x-codex-([a-z0-9][a-z0-9_-]*)-(used-percent|remaining-percent|limit|credits|credits-remaining|reset-at|window-id|window)$`)

// accountWindowSnapshots parses only bounded, recognized Codex quota headers.
// It returns one row per pool and retains exact decimal lexemes through the
// shared Decimal parser. Invalid values are omitted, never converted into a
// request charge or a negative reset/refund.
func accountWindowSnapshots(headers http.Header, accountID string, observedAt time.Time) []AccountWindowSnapshot {
	if len(headers) == 0 {
		return nil
	}
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	} else {
		observedAt = observedAt.UTC()
	}
	values := make(map[string]map[string]string)
	for key, entries := range headers {
		lk := strings.ToLower(strings.TrimSpace(key))
		match := codexWindowHeaderPattern.FindStringSubmatch(lk)
		if len(match) != 3 || len(entries) == 0 {
			continue
		}
		value := strings.TrimSpace(entries[0])
		if value == "" || len(value) > 256 {
			continue
		}
		if values[match[1]] == nil {
			values[match[1]] = make(map[string]string)
		}
		values[match[1]][match[2]] = value
	}
	poolIDs := make([]string, 0, len(values))
	for pool := range values {
		poolIDs = append(poolIDs, pool)
	}
	sort.Strings(poolIDs)
	out := make([]AccountWindowSnapshot, 0, len(poolIDs))
	for _, pool := range poolIDs {
		row := values[pool]
		snapshot := AccountWindowSnapshot{
			ProviderAccountKey: strings.TrimSpace(accountID), PoolID: pool,
			ObservedAt: observedAt, Semantics: aipsdkmetering.SemanticsGauge,
			Origin: aipsdkmetering.OriginProvider, Acquisition: aipsdkmetering.AcquisitionProviderHeader,
			Authority: aipsdkmetering.AuthorityObservedClaim,
		}
		snapshot.WindowID = firstWindowValue(row)
		snapshot.ResetAt = parseResetAt(row["reset-at"])
		if snapshot.WindowID == "" && !snapshot.ResetAt.IsZero() {
			snapshot.WindowID = pool + "@" + strconv.FormatInt(snapshot.ResetAt.Unix(), 10)
		}
		snapshot.UsedPercent = parseHeaderDecimal(row["used-percent"])
		snapshot.RemainingPercent = parseHeaderDecimal(row["remaining-percent"])
		snapshot.Limit = parseHeaderDecimal(row["limit"])
		snapshot.Credits = parseHeaderDecimal(firstNonEmptyWindow(row["credits-remaining"], row["credits"]))
		if snapshot.UsedPercent == nil && snapshot.RemainingPercent == nil && snapshot.Limit == nil && snapshot.Credits == nil && snapshot.WindowID == "" && snapshot.ResetAt.IsZero() {
			continue
		}
		out = append(out, snapshot)
	}
	return out
}

func firstWindowValue(values map[string]string) string {
	for _, key := range []string{"window-id", "window"} {
		if value := strings.TrimSpace(values[key]); value != "" {
			return value
		}
	}
	return ""
}

func firstNonEmptyWindow(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func parseHeaderDecimal(raw string) *aipsdkmetering.Decimal {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "-") {
		return nil
	}
	value, err := aipsdkmetering.ParseDecimal(raw)
	if err != nil {
		return nil
	}
	return &value
}

func parseResetAt(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil && seconds >= 0 {
		return time.Unix(seconds, 0).UTC()
	}
	if value, err := time.Parse(time.RFC3339, raw); err == nil {
		return value.UTC()
	}
	return time.Time{}
}
