package ledgerstore

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"sort"
	"strconv"
	"strings"
	"time"

	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// cursorPayload is the internal, opaque-to-consumers continuation token. It
// binds the resume position (LastSeq) to the query shape hash and visibility
// so a cursor cannot be reused across different query conditions (requirement
// 2.7, 7.4).
type cursorPayload struct {
	LastSeq    int64  `json:"last_seq"`
	ShapeHash  uint64 `json:"shape_hash"`
	Visibility string `json:"visibility"`
}

// IsZero reports whether the cursor payload carries no resume position.
func (p cursorPayload) IsZero() bool {
	return p.LastSeq == 0 && p.ShapeHash == 0 && p.Visibility == ""
}

// encodeCursor marshals the payload to an opaque base64url string. Empty
// LastSeq with zero shape hash produces an empty (zero) cursor.
func encodeCursor(p cursorPayload) cp.Cursor {
	if p.LastSeq == 0 && p.ShapeHash == 0 && p.Visibility == "" {
		return cp.Cursor{}
	}
	b, err := json.Marshal(p)
	if err != nil {
		return cp.Cursor{}
	}
	return cp.Cursor{Token: base64.RawURLEncoding.EncodeToString(b)}
}

// decodeCursor parses an opaque token. Empty token yields a zero payload. A
// malformed token yields an error so callers can classify it as invalid query.
func decodeCursor(c cp.Cursor) (cursorPayload, error) {
	if c.IsZero() {
		return cursorPayload{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(c.Token)
	if err != nil {
		return cursorPayload{}, fmt.Errorf("ledgerstore: decode cursor: %w", err)
	}
	var p cursorPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return cursorPayload{}, fmt.Errorf("ledgerstore: unmarshal cursor: %w", err)
	}
	return p, nil
}

// shapeHash computes a stable FNV-1a hash over the canonical query shape so
// continuation tokens are bound to the filters and visibility that produced
// them (requirement 2.7). The cursor token itself is excluded.
func shapeHash(q cp.EventQuery) uint64 {
	h := fnv.New64a()
	writeString(h, "event|")
	writeString(h, string(q.Category))
	writeString(h, "|vis:")
	writeString(h, string(q.Visibility))
	writeString(h, "|limit:")
	writeInt(h, int64(q.Limit))
	writeCommonShape(h, q.Common)
	_ = q.Cursor // cursor intentionally excluded from shape
	return h.Sum64()
}

func shapeHashSession(q cp.SessionQuery) uint64 {
	h := fnv.New64a()
	writeString(h, "session|vis:")
	writeString(h, string(q.Visibility))
	writeString(h, "|limit:")
	writeInt(h, int64(q.Limit))
	writeCommonShape(h, q.Common)
	return h.Sum64()
}

func shapeHashAttempt(q cp.AttemptQuery) uint64 {
	h := fnv.New64a()
	writeString(h, "attempt|surfaced:")
	writeString(h, q.Surfaced)
	writeString(h, "|vis:")
	writeString(h, string(q.Visibility))
	writeString(h, "|limit:")
	writeInt(h, int64(q.Limit))
	writeCommonShape(h, q.Common)
	return h.Sum64()
}

func shapeHashUsage(q cp.UsageQuery) uint64 {
	h := fnv.New64a()
	writeString(h, "usage|plane:")
	writeString(h, q.Plane)
	writeString(h, "|avail:")
	writeString(h, q.Availability)
	writeString(h, "|perspective:")
	writeString(h, string(q.Perspective))
	writeString(h, "|boundary:")
	writeString(h, string(q.Boundary))
	writeString(h, "|lifecycle:")
	writeString(h, string(q.LifecycleScope))
	writeString(h, "|rule:")
	writeString(h, q.RuleID)
	writeString(h, "|class:")
	writeString(h, string(q.Class))
	writeString(h, "|vis:")
	writeString(h, string(q.Visibility))
	writeString(h, "|limit:")
	writeInt(h, int64(q.Limit))
	writeCommonShape(h, q.Common)
	return h.Sum64()
}

func shapeHashUsageAggregate(q cp.UsageAggregateQuery) uint64 {
	h := fnv.New64a()
	writeString(h, "usage_agg|vis:")
	writeString(h, string(q.Visibility))
	writeString(h, "|limit:")
	writeInt(h, int64(q.Limit))
	writeString(h, "|group_by:")
	writeString(h, strings.Join(sortedClone(q.GroupBy), ","))
	writeCommonShape(h, q.Common)
	return h.Sum64()
}

func shapeHashEvidence(q cp.EvidenceQuery) uint64 {
	h := fnv.New64a()
	writeString(h, "evidence|effect:")
	writeString(h, q.Effect)
	writeString(h, "|cat:")
	writeString(h, string(q.Category))
	writeString(h, "|vis:")
	writeString(h, string(q.Visibility))
	writeString(h, "|limit:")
	writeInt(h, int64(q.Limit))
	writeCommonShape(h, q.Common)
	return h.Sum64()
}

func writeCommonShape(h interface {
	Write([]byte) (int, error)
}, c cp.CommonFilters,
) {
	writeString(h, "|scope:")
	writeScopeShape(h, c.Scope)
	writeString(h, "|t:")
	writeTimeShape(h, c.TimeRange.From)
	writeTimeShape(h, c.TimeRange.To)
	writeString(h, "|be:")
	writeString(h, c.BackendID)
	writeString(h, "|md:")
	writeString(h, c.Model)
	writeString(h, "|fe:")
	writeString(h, c.FrontendID)
	writeString(h, "|tr:")
	writeString(h, c.TraceID)
	writeString(h, "|ss:")
	writeString(h, c.SessionID)
	writeString(h, "|al:")
	writeString(h, c.ALegID)
	writeString(h, "|bl:")
	writeString(h, c.BLegID)
	writeString(h, "|oc:")
	writeString(h, c.Outcome)
	writeString(h, "|rc:")
	writeString(h, c.ReasonCode)
}

func writeScopeShape(h interface {
	Write([]byte) (int, error)
}, s cp.ScopeFilters,
) {
	writeScopeValue(h, "p", s.PrincipalID)
	writeScopeValue(h, "c", s.CredentialID)
	writeScopeValue(h, "t", s.TenantID)
	writeScopeValue(h, "o", s.OrganizationID)
	writeScopeValue(h, "w", s.WorkspaceID)
	writeScopeValue(h, "pr", s.ProjectID)
	writeScopeValue(h, "d", s.DepartmentID)
	writeScopeValue(h, "cc", s.CostCenterID)
}

func writeScopeValue(h interface {
	Write([]byte) (int, error)
}, label string, v scope.Value,
) {
	writeString(h, label)
	if v.IsUnknown() {
		writeString(h, "U")
		return
	}
	writeString(h, "K:")
	writeString(h, v.String())
	writeString(h, ";")
}

// writeTimeShape hashes the actual time value into the query shape so cursors
// are bound to the specific time bounds that produced them, not just their
// presence (requirement 2.7, 7.4). The zero time and a non-zero time whose
// UnixNano is 0 (time.Unix(0, 0)) use distinct encodings so they cannot
// collide accidentally.
func writeTimeShape(h interface {
	Write([]byte) (int, error)
}, t time.Time,
) {
	if t.IsZero() {
		writeString(h, "Z")
		return
	}
	writeString(h, "V:")
	writeInt(h, t.UnixNano())
}

func writeString(h interface {
	Write([]byte) (int, error)
}, s string,
) {
	_, _ = h.Write([]byte(s))
}

func writeInt(h interface {
	Write([]byte) (int, error)
}, n int64,
) {
	var buf [20]byte
	_, _ = h.Write(strconv.AppendInt(buf[:0], n, 10))
}

func sortedClone(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
