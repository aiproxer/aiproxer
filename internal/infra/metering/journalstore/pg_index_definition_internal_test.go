package journalstore

import (
	"strings"
	"testing"
)

const postgresIndexExpectedColumns = "store_id, b_leg_id, stream_id, sequence, observation_id, observation_revision, id"

// TestCheckPostgresIndexDefinition covers the pure semantic comparison with
// realistic PostgreSQL pg_indexes renderings (type casts and parentheses). The
// negative cases are the reviewer's rejection scenario: an extra restriction or
// a disjunction must not satisfy verification even though the index name and the
// leading columns are unchanged. It also pins the access-method contract: only
// btree can serve the bounded ordered lookup, so a non-btree rendering of the
// pinned index must be rejected, while semantically correct btree renderings
// (clause omitted, mixed case, quoted index name) must not be rejected on
// formatting alone.
func TestCheckPostgresIndexDefinition(t *testing.T) {
	blegPredicates := []string{"payload_kind = 'observation'"}
	candidatePredicates := []string{
		"payload_kind = 'observation'",
		"observation_subject_kind = 'statement_line'",
		"observation_origin = 'statement'",
		"observation_acquisition = 'statement_importer'",
		"authority = 'verified_statement'",
	}
	const (
		candidatePrefix = `CREATE INDEX idx_metering_facts_store_bleg_statement ON public.metering_facts USING btree (` +
			postgresIndexExpectedColumns + `) WHERE (`
		blegPrefix = `CREATE INDEX idx_metering_facts_store_bleg ON public.metering_facts USING btree (` +
			postgresIndexExpectedColumns + `) WHERE (`
		// blegHead/blegBody split the same index definition so the access-method
		// cases below differ from the accepted b-tree case in exactly one token
		// and can only be rejected because of that token.
		blegHead = `CREATE INDEX idx_metering_facts_store_bleg ON public.metering_facts `
		blegBody = postgresIndexExpectedColumns + `) WHERE (payload_kind = 'observation'::text)`
	)
	cases := []struct {
		name    string
		raw     string
		columns string
		// wantErrs are substrings that must all appear in the error. An empty
		// slice means the definition must verify.
		predicates []string
		wantErrs   []string
	}{
		{
			name:       "bleg exact",
			raw:        blegPrefix + `payload_kind = 'observation'::text)`,
			columns:    postgresIndexExpectedColumns,
			predicates: blegPredicates,
		},
		{
			name: "candidate exact",
			raw: candidatePrefix +
				`(payload_kind = 'observation'::text) AND (observation_subject_kind = 'statement_line'::text)` +
				` AND (observation_origin = 'statement'::text) AND (observation_acquisition = 'statement_importer'::text)` +
				` AND (authority = 'verified_statement'::text))`,
			columns:    postgresIndexExpectedColumns,
			predicates: candidatePredicates,
		},
		{
			name: "candidate missing conjunct rejected",
			raw: candidatePrefix +
				`(payload_kind = 'observation'::text) AND (observation_subject_kind = 'statement_line'::text)` +
				` AND (observation_origin = 'statement'::text) AND (observation_acquisition = 'statement_importer'::text))`,
			columns:    postgresIndexExpectedColumns,
			predicates: candidatePredicates,
			wantErrs:   []string{"partial predicate"},
		},
		{
			name: "candidate extra conjunct rejected",
			raw: candidatePrefix +
				`(payload_kind = 'observation'::text) AND (observation_subject_kind = 'statement_line'::text)` +
				` AND (observation_origin = 'statement'::text) AND (observation_acquisition = 'statement_importer'::text)` +
				` AND (authority = 'verified_statement'::text) AND (b_leg_id = 'one-leg'::text))`,
			columns:    postgresIndexExpectedColumns,
			predicates: candidatePredicates,
			wantErrs:   []string{"partial predicate"},
		},
		{
			name: "candidate disjunction rejected",
			raw: candidatePrefix +
				`(payload_kind = 'observation'::text) AND (observation_subject_kind = 'statement_line'::text)` +
				` AND (observation_origin = 'statement'::text) AND (observation_acquisition = 'statement_importer'::text)` +
				` OR (authority = 'verified_statement'::text))`,
			columns:    postgresIndexExpectedColumns,
			predicates: candidatePredicates,
			wantErrs:   []string{"partial predicate"},
		},
		{
			// Truncated key list, not a reordering: (store_id, b_leg_id) drops
			// every ORDER BY tie-breaker column.
			name:       "missing key columns rejected",
			raw:        `CREATE INDEX idx_metering_facts_store_bleg ON public.metering_facts USING btree (store_id, b_leg_id) WHERE (payload_kind = 'observation'::text)`,
			columns:    postgresIndexExpectedColumns,
			predicates: blegPredicates,
			wantErrs:   []string{"key columns"},
		},
		{
			// Same columns, b_leg_id ahead of store_id: the (store_id, b_leg_id)
			// equality prefix the bounded lookup binds is no longer the leading
			// btree prefix, so the index cannot serve the planned scan.
			name:       "permuted leading key columns rejected",
			raw:        blegHead + `USING btree (` + movePostgresIndexColumn(t, 1, 0) + `) WHERE (payload_kind = 'observation'::text)`,
			columns:    postgresIndexExpectedColumns,
			predicates: blegPredicates,
			wantErrs:   []string{"key columns"},
		},
		{
			// Same columns, the id tie-breaker promoted ahead of the ORDER BY
			// prefix: an ordered set comparison would accept this, and the
			// bounded scan would fall back to a temp B-tree sort.
			name:       "permuted tie-break key columns rejected",
			raw:        blegHead + `USING btree (` + movePostgresIndexColumn(t, 6, 2) + `) WHERE (payload_kind = 'observation'::text)`,
			columns:    postgresIndexExpectedColumns,
			predicates: blegPredicates,
			wantErrs:   []string{"key columns"},
		},
		{
			name:       "literal case mismatch rejected",
			raw:        blegPrefix + `payload_kind = 'OBSERVATION'::text)`,
			columns:    postgresIndexExpectedColumns,
			predicates: blegPredicates,
			wantErrs:   []string{"partial predicate"},
		},
		{
			name:       "literal containing cast text rejected",
			raw:        blegPrefix + `payload_kind = 'observation::text'::text)`,
			columns:    postgresIndexExpectedColumns,
			predicates: blegPredicates,
			wantErrs:   []string{"partial predicate"},
		},
		{
			// A same-named index created with a non-btree access method keeps the
			// ordered key list and the partial predicate, so the only evidence
			// that it cannot serve the ordered bounded lookup is the method.
			name:       "brin access method rejected",
			raw:        blegHead + `USING brin (` + blegBody,
			columns:    postgresIndexExpectedColumns,
			predicates: blegPredicates,
			wantErrs:   []string{`index access method "brin" is not supported`, `want "btree"`},
		},
		{
			name:       "gist access method rejected",
			raw:        blegHead + `USING gist (` + blegBody,
			columns:    postgresIndexExpectedColumns,
			predicates: blegPredicates,
			wantErrs:   []string{`index access method "gist" is not supported`, `want "btree"`},
		},
		{
			name:       "gin access method rejected",
			raw:        blegHead + `USING gin (` + blegBody,
			columns:    postgresIndexExpectedColumns,
			predicates: blegPredicates,
			wantErrs:   []string{`index access method "gin" is not supported`, `want "btree"`},
		},
		{
			name:       "hash access method rejected",
			raw:        blegHead + `USING hash (` + blegBody,
			columns:    postgresIndexExpectedColumns,
			predicates: blegPredicates,
			wantErrs:   []string{`index access method "hash" is not supported`, `want "btree"`},
		},
		{
			// "using" is optional DDL: the btree default applies when it is
			// omitted, so a semantically correct index must still verify.
			name:       "btree access method omitted accepted",
			raw:        blegHead + `(` + blegBody,
			columns:    postgresIndexExpectedColumns,
			predicates: blegPredicates,
		},
		{
			// The comparison must not depend on the rendering case of the
			// access-method keyword.
			name:       "uppercase btree access method accepted",
			raw:        blegHead + `USING BTREE (` + blegBody,
			columns:    postgresIndexExpectedColumns,
			predicates: blegPredicates,
		},
		{
			// "using" is a reserved word, so an index of that name is stored and
			// rendered by pg_get_indexdef as the quoted identifier "using". A
			// quoted name is one token and must not be read as the clause.
			name:       "quoted using index name accepted",
			raw:        `CREATE INDEX "using" ON public.metering_facts USING btree (` + blegBody,
			columns:    postgresIndexExpectedColumns,
			predicates: blegPredicates,
		},
		{
			// Same quoted name with the clause omitted: the word appears in the
			// definition and there is no real clause at all.
			name:       "quoted using index name without access method accepted",
			raw:        `CREATE INDEX "using" ON public.metering_facts (` + blegBody,
			columns:    postgresIndexExpectedColumns,
			predicates: blegPredicates,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkPostgresIndexDefinition(tc.raw, tc.columns, tc.predicates)
			if len(tc.wantErrs) == 0 {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q", tc.wantErrs)
			}
			for _, want := range tc.wantErrs {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q does not contain %q", err, want)
				}
			}
		})
	}
}

// movePostgresIndexColumn returns postgresIndexExpectedColumns with the key
// column at from moved to position to, so the permutation negative cases stay a
// faithful reordering of the real list: a hand-copied literal would silently rot
// into the expected order itself, or into a truncation, the moment the pinned
// key list changes.
func movePostgresIndexColumn(t *testing.T, from, to int) string {
	t.Helper()
	columns := strings.Split(postgresIndexExpectedColumns, ", ")
	if from < 0 || to < 0 || from >= len(columns) || to >= len(columns) {
		t.Fatalf("postgresIndexExpectedColumns has %d columns, cannot move %d to %d", len(columns), from, to)
	}
	column := columns[from]
	rest := append(columns[:from:from], columns[from+1:]...)
	reordered := make([]string, 0, len(columns))
	reordered = append(reordered, rest[:to]...)
	reordered = append(reordered, column)
	reordered = append(reordered, rest[to:]...)
	permuted := strings.Join(reordered, ", ")
	if permuted == postgresIndexExpectedColumns {
		t.Fatalf("permuting column %d to %d did not change the key list", from, to)
	}
	return permuted
}

// TestCanonicalizePostgresIndexExpressionPreservesLiterals proves syntax
// normalization (lower-casing, cast stripping, parenthesis removal, whitespace
// collapsing) applies only outside single-quoted SQL literals; literal bytes,
// including case, embedded cast-looking text, doubled quotes, and spaces, are
// preserved exactly.
func TestCanonicalizePostgresIndexExpressionPreservesLiterals(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`payload_kind = 'OBSERVATION'::text`, `payload_kind = 'OBSERVATION'`},
		{`payload_kind = 'observation::text'::text`, `payload_kind = 'observation::text'`},
		{`note = 'it''s fine'::text`, `note = 'it''s fine'`},
		{`(a = 'X'::text) AND (b = 'y'::text)`, `a = 'X' and b = 'y'`},
	}
	for _, tc := range cases {
		if got := canonicalizePostgresIndexExpression(tc.in); got != tc.want {
			t.Errorf("canonicalizePostgresIndexExpression(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
