package proxymemory

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// Service fixtures. The scope identifiers are opaque 64-hex project keys, the
// same shape the capture plane derives and the service validates.
const (
	serviceTestTenant    = "tenant-service"
	serviceTestPrincipal = "alice-service"
	serviceTestScope     = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	serviceTestAltScope  = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"

	serviceTestAlphaSummary   = "2026-01-01T00-00-00-012345-alpha.md"
	serviceTestBravoSummary   = "2026-01-02T00-00-00-012345-bravo.md"
	serviceTestCharlieSummary = "2026-01-03T00-00-00-012345-charlie.md"
)

// serviceTestTime is the deterministic control-path clock of the service
// tests.
var serviceTestTime = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

func serviceTestNow() time.Time { return serviceTestTime }

// serviceTestCaller is the caller principal every service test acts as.
func serviceTestCaller() Principal {
	return Principal{TenantID: serviceTestTenant, PrincipalID: serviceTestPrincipal}
}

// serviceTestOwner is the owner triple that caller and serviceTestScope
// resolve to.
func serviceTestOwner() Owner {
	return Owner{TenantID: serviceTestTenant, PrincipalID: serviceTestPrincipal, ProjectKey: serviceTestScope}
}

// serviceTestConfig enables one principal and keeps the documented limits.
func serviceTestConfig() Config {
	return Config{
		Principals:       []string{serviceTestPrincipal},
		ExtractRoute:     "route-service-extract",
		ConsolidateRoute: "route-service-consolidate",
		MaxNoteBytes:     defaultMaxNoteBytes,
		MaxReadBytes:     defaultMaxReadBytes,
		MaxSearchResults: defaultMaxSearchResults,
	}
}

// serviceTestDocuments is the document set of the read and search tests: two
// fixed documents plus three rollout summaries whose map order differs from
// their name order.
func serviceTestDocuments() Documents {
	return Documents{
		MemoryMD:      "alpha first\nsecond ALPHA line\nunrelated\nneedle here\n" + strings.Repeat("n", 300) + "\n",
		MemorySummary: "the summary\na summary needle\n",
		RolloutSummaries: map[string]string{
			serviceTestCharlieSummary: "rollout charlie\n",
			serviceTestAlphaSummary:   "rollout needle one\n",
			serviceTestBravoSummary:   "rollout needle two\n",
		},
		ProjectLabel: "/w/service",
	}
}

// serviceStoreStub is the in-memory Store of the service tests. The service is
// synchronous, so the stub needs no locking: it records every call the service
// makes, answers from canned data and reports injected failures. The embedded
// Store stays nil, so any call outside the service's own surface panics
// loudly instead of silently passing.
type serviceStoreStub struct {
	Store

	order  []string
	owners []Owner

	documents    Documents
	documentsErr error

	bankStatus    BankStatus
	bankStatusErr error

	publishedSummary    string
	publishedVersion    int64
	publishedSummaryErr error

	listBanks           []BankStatus
	listBanksPrincipals []Principal
	listBanksErr        error

	addedOwners []Owner
	addedNotes  []Note
	addNoteErr  error

	forgetOwners []Owner
	forgetNowMS  []int64
	forgetErr    error

	overrides           OwnerOverrides
	overridesErr        error
	overridesPrincipals []Principal
	writtenOverrides    []OwnerOverrides
	writtenSettings     []StoredOwnerSettings
	writtenPrincipals   []Principal
	writeOverridesErr   error
	deletedPrincipals   []Principal
	deleteOverridesErr  error
}

func newServiceStoreStub() *serviceStoreStub { return &serviceStoreStub{} }

// note records one store call. noteOwner additionally records the owner the
// call received, so the tests can prove every scoped call carries the caller's
// triple.
func (s *serviceStoreStub) note(method string) { s.order = append(s.order, method) }

func (s *serviceStoreStub) noteOwner(o Owner) { s.owners = append(s.owners, o) }

// documentsCalls counts how many times the store was asked for documents.
func (s *serviceStoreStub) documentsCalls() int {
	count := 0
	for _, method := range s.order {
		if method == "Documents" {
			count++
		}
	}
	return count
}

func (s *serviceStoreStub) ListBanks(_ context.Context, p Principal) ([]BankStatus, error) {
	s.note("ListBanks")
	s.listBanksPrincipals = append(s.listBanksPrincipals, p)
	return s.listBanks, s.listBanksErr
}

func (s *serviceStoreStub) BankStatus(_ context.Context, o Owner) (BankStatus, error) {
	s.note("BankStatus")
	s.noteOwner(o)
	return s.bankStatus, s.bankStatusErr
}

func (s *serviceStoreStub) PublishedSummary(_ context.Context, o Owner) (string, int64, error) {
	s.note("PublishedSummary")
	s.noteOwner(o)
	return s.publishedSummary, s.publishedVersion, s.publishedSummaryErr
}

func (s *serviceStoreStub) Documents(_ context.Context, o Owner) (Documents, error) {
	s.note("Documents")
	s.noteOwner(o)
	return s.documents, s.documentsErr
}

func (s *serviceStoreStub) AddNote(_ context.Context, o Owner, n Note) error {
	s.note("AddNote")
	s.noteOwner(o)
	s.addedOwners = append(s.addedOwners, o)
	s.addedNotes = append(s.addedNotes, n)
	return s.addNoteErr
}

func (s *serviceStoreStub) Forget(_ context.Context, o Owner, nowMS int64) error {
	s.note("Forget")
	s.noteOwner(o)
	s.forgetOwners = append(s.forgetOwners, o)
	s.forgetNowMS = append(s.forgetNowMS, nowMS)
	return s.forgetErr
}

// ReadSettings answers with the stub's stored settings and records the
// principal the call carried.
func (s *serviceStoreStub) ReadSettings(_ context.Context, p Principal) (StoredOwnerSettings, error) {
	s.note("ReadSettings")
	s.overridesPrincipals = append(s.overridesPrincipals, p)
	return StoredOwnerSettings{
		Extraction:    StoredPhaseSettings{Overrides: s.overrides.Extraction},
		Consolidation: StoredPhaseSettings{Overrides: s.overrides.Consolidation},
	}, s.overridesErr
}

// WriteSettings records the accepted settings and the principal.
func (s *serviceStoreStub) WriteSettings(_ context.Context, p Principal, value StoredOwnerSettings) error {
	s.note("WriteSettings")
	s.writtenPrincipals = append(s.writtenPrincipals, p)
	s.writtenOverrides = append(s.writtenOverrides, value.Overrides())
	s.writtenSettings = append(s.writtenSettings, value)
	return s.writeOverridesErr
}

// DeleteSettings records the principal whose settings were revoked.
func (s *serviceStoreStub) DeleteSettings(_ context.Context, p Principal) error {
	s.note("DeleteSettings")
	s.deletedPrincipals = append(s.deletedPrincipals, p)
	return s.deleteOverridesErr
}

// assertServiceOwners requires that every owner the store received is the
// caller's triple.
func assertServiceOwners(t *testing.T, got []Owner, want Owner) {
	t.Helper()
	if len(got) == 0 {
		t.Fatalf("the store received no owner, want %+v", want)
	}
	for i, owner := range got {
		if owner != want {
			t.Fatalf("store owner %d = %+v, want %+v", i, owner, want)
		}
	}
}

// serviceNoteIDPattern is the design §11.1 note identifier: 16 random bytes,
// lowercase hex.
var serviceNoteIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// TestServiceCaller pins that Caller is CheckEligible without a project.
func TestServiceCaller(t *testing.T) {
	svc := NewService(serviceTestConfig(), newServiceStoreStub(), nil, serviceTestNow)
	cases := []struct {
		name string
		view scope.PrincipalScopeView
		want Principal
		ok   bool
	}{
		{
			name: "allowlisted principal",
			view: scope.PrincipalScopeView{
				SubjectKind: scope.SubjectHuman,
				PrincipalID: scope.Known(serviceTestPrincipal),
				TenantID:    scope.Known(serviceTestTenant),
			},
			want: serviceTestCaller(),
			ok:   true,
		},
		{
			name: "not allowlisted",
			view: scope.PrincipalScopeView{
				SubjectKind: scope.SubjectHuman,
				PrincipalID: scope.Known("bob-service"),
				TenantID:    scope.Known(serviceTestTenant),
			},
			ok: false,
		},
		{
			name: "internal origin",
			view: scope.PrincipalScopeView{
				SubjectKind: scope.SubjectService,
				PrincipalID: scope.Known(serviceTestPrincipal),
				Origin:      scope.OriginInternal,
			},
			ok: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := svc.Caller(tc.view)
			if ok != tc.ok {
				t.Fatalf("Caller eligibility = %v, want %v", ok, tc.ok)
			}
			if got != tc.want {
				t.Fatalf("Caller principal = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestServiceScopeIDIsValidatedBeforeAnyStoreCall pins design §11.1: a scope
// identifier that is not 64 lowercase hexadecimal characters is not found, and
// the store is never asked about it.
func TestServiceScopeIDIsValidatedBeforeAnyStoreCall(t *testing.T) {
	cases := []struct {
		name    string
		scopeID string
	}{
		{"empty", ""},
		{"short", "0123456789abcdef"},
		{"63 characters", strings.Repeat("0", 63)},
		{"65 characters", strings.Repeat("0", 65)},
		{"uppercase hex", strings.Repeat("A", 64)},
		{"non hex", strings.Repeat("g", 64)},
		{"newline byte", strings.Repeat("a", 63) + "\n"},
		{"leading space", " " + serviceTestScope},
		{"trailing space", serviceTestScope + " "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := newServiceStoreStub()
			svc := NewService(serviceTestConfig(), stub, nil, serviceTestNow)
			ctx := context.Background()
			p := serviceTestCaller()

			if _, _, err := svc.Status(ctx, p, tc.scopeID); !errors.Is(err, ErrNotFound) {
				t.Errorf("Status error = %v, want ErrNotFound", err)
			}
			if _, err := svc.Paths(ctx, p, tc.scopeID); !errors.Is(err, ErrNotFound) {
				t.Errorf("Paths error = %v, want ErrNotFound", err)
			}
			if _, err := svc.Read(ctx, p, tc.scopeID, documentMemoryMD, 0, 0); !errors.Is(err, ErrNotFound) {
				t.Errorf("Read error = %v, want ErrNotFound", err)
			}
			if _, err := svc.Search(ctx, p, tc.scopeID, "needle"); !errors.Is(err, ErrNotFound) {
				t.Errorf("Search error = %v, want ErrNotFound", err)
			}
			if err := svc.AddNote(ctx, p, tc.scopeID, "note"); !errors.Is(err, ErrNotFound) {
				t.Errorf("AddNote error = %v, want ErrNotFound", err)
			}
			if err := svc.Forget(ctx, p, tc.scopeID); !errors.Is(err, ErrNotFound) {
				t.Errorf("Forget error = %v, want ErrNotFound", err)
			}
			if len(stub.order) != 0 {
				t.Fatalf("store calls = %v, want none for malformed scope ID %q", stub.order, tc.scopeID)
			}
		})
	}

	t.Run("malformed scope wins over an invalid query", func(t *testing.T) {
		stub := newServiceStoreStub()
		svc := NewService(serviceTestConfig(), stub, nil, serviceTestNow)
		if _, err := svc.Search(context.Background(), serviceTestCaller(), "not-a-scope", ""); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Search error = %v, want ErrNotFound", err)
		}
		if len(stub.order) != 0 {
			t.Fatalf("store calls = %v, want none", stub.order)
		}
	})

	t.Run("a well-formed scope reaches the store", func(t *testing.T) {
		stub := newServiceStoreStub()
		stub.documents = serviceTestDocuments()
		svc := NewService(serviceTestConfig(), stub, nil, serviceTestNow)
		if _, err := svc.Paths(context.Background(), serviceTestCaller(), serviceTestScope); err != nil {
			t.Fatalf("Paths error = %v, want nil", err)
		}
		assertServiceOwners(t, stub.owners, serviceTestOwner())
	})
}

// TestServiceRead pins design §11.1's document resolution and line ranges.
func TestServiceRead(t *testing.T) {
	docs := Documents{
		MemoryMD:      "alpha\nbeta\ngamma\n",
		MemorySummary: "the summary\n",
		RolloutSummaries: map[string]string{
			serviceTestAlphaSummary: "alpha rollout\n",
			serviceTestBravoSummary: "bravo rollout\n",
		},
		ProjectLabel: "/w/service",
	}
	cases := []struct {
		name      string
		path      string
		startLine int
		endLine   int
		want      string
		wantErr   error
	}{
		{"whole document", documentMemoryMD, 0, 0, "1: alpha\n2: beta\n3: gamma\n", nil},
		{"closed range", documentMemoryMD, 2, 3, "2: beta\n3: gamma\n", nil},
		{"start only", documentMemoryMD, 2, 0, "2: beta\n3: gamma\n", nil},
		{"end only", documentMemoryMD, 0, 1, "1: alpha\n", nil},
		{"range past the end", documentMemoryMD, 9, 0, "", nil},
		{"reversed range", documentMemoryMD, 3, 1, "", nil},
		{"summary document", documentMemorySummary, 0, 0, "1: the summary\n", nil},
		{"rollout summary", "rollout_summaries/" + serviceTestAlphaSummary, 0, 0, "1: alpha rollout\n", nil},
		{"unknown path", "notes.md", 0, 0, "", ErrNotFound},
		{"rollout folder path", "rollout_summaries/", 0, 0, "", ErrNotFound},
		{"unknown rollout summary", "rollout_summaries/" + serviceTestCharlieSummary, 0, 0, "", ErrNotFound},
		{"traversal path", "rollout_summaries/../" + documentMemoryMD, 0, 0, "", ErrNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := newServiceStoreStub()
			stub.documents = docs
			svc := NewService(serviceTestConfig(), stub, nil, serviceTestNow)

			got, err := svc.Read(context.Background(), serviceTestCaller(), serviceTestScope, tc.path, tc.startLine, tc.endLine)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Read(%q, %d, %d) error = %v, want %v", tc.path, tc.startLine, tc.endLine, err, tc.wantErr)
			}
			if tc.wantErr == nil && got != tc.want {
				t.Fatalf("Read(%q, %d, %d) = %q, want %q", tc.path, tc.startLine, tc.endLine, got, tc.want)
			}
			assertServiceOwners(t, stub.owners, serviceTestOwner())
		})
	}

	t.Run("store error propagates", func(t *testing.T) {
		stub := newServiceStoreStub()
		cause := errors.New("store down")
		stub.documentsErr = cause
		svc := NewService(serviceTestConfig(), stub, nil, serviceTestNow)
		if _, err := svc.Read(context.Background(), serviceTestCaller(), serviceTestScope, documentMemoryMD, 0, 0); !errors.Is(err, cause) {
			t.Fatalf("Read error = %v, want %v", err, cause)
		}
	})

	t.Run("not found propagates", func(t *testing.T) {
		stub := newServiceStoreStub()
		stub.documentsErr = ErrNotFound
		svc := NewService(serviceTestConfig(), stub, nil, serviceTestNow)
		if _, err := svc.Read(context.Background(), serviceTestCaller(), serviceTestScope, documentMemoryMD, 0, 0); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Read error = %v, want ErrNotFound", err)
		}
	})
}

// TestServiceReadTruncation pins the MaxReadBytes cap and its final marker
// line (Requirement 7.3).
func TestServiceReadTruncation(t *testing.T) {
	t.Run("cut at MaxReadBytes with the marker line", func(t *testing.T) {
		stub := newServiceStoreStub()
		stub.documents = Documents{MemoryMD: "first line\n" + strings.Repeat("y", 500) + "\ntail line\n"}
		cfg := serviceTestConfig()
		cfg.MaxReadBytes = 200
		svc := NewService(cfg, stub, nil, serviceTestNow)

		got, err := svc.Read(context.Background(), serviceTestCaller(), serviceTestScope, documentMemoryMD, 0, 0)
		if err != nil {
			t.Fatalf("Read error = %v, want nil", err)
		}
		if len(got) > cfg.MaxReadBytes {
			t.Fatalf("len(Read) = %d, want at most MaxReadBytes %d", len(got), cfg.MaxReadBytes)
		}
		if !strings.HasPrefix(got, "1: first line\n2: ") {
			t.Fatalf("Read = %q, want numbered lines from the beginning", got)
		}
		if !strings.HasSuffix(got, "…[truncated]\n") {
			t.Fatalf("Read = %q, want the final …[truncated] line", got)
		}
		if strings.Contains(got, "tail line") {
			t.Fatalf("Read = %q contains content beyond the cut", got)
		}
	})

	t.Run("no marker when the output fits", func(t *testing.T) {
		stub := newServiceStoreStub()
		stub.documents = Documents{MemoryMD: "one\ntwo\n"}
		svc := NewService(serviceTestConfig(), stub, nil, serviceTestNow)

		got, err := svc.Read(context.Background(), serviceTestCaller(), serviceTestScope, documentMemoryMD, 0, 0)
		if err != nil {
			t.Fatalf("Read error = %v, want nil", err)
		}
		if want := "1: one\n2: two\n"; got != want {
			t.Fatalf("Read = %q, want %q", got, want)
		}
	})
}

// TestServiceReadCutRedactsSecrets pins the Requirement 3.3 wrap on the read
// cut: the returned value is sanitized before the cut (so no fragment of a
// secret the original matched can survive) and again after it (so a boundary
// the cut creates cannot leak).
func TestServiceReadCutRedactsSecrets(t *testing.T) {
	t.Run("no fragment of a secret straddling the cut", func(t *testing.T) {
		stub := newServiceStoreStub()
		stub.documents = Documents{
			MemoryMD: strings.Repeat("a", 160) + "sk-ABCDEFGHIJKLMNOPQRSTUVWX" + strings.Repeat("b", 120) + "\n",
		}
		cfg := serviceTestConfig()
		cfg.MaxReadBytes = 200
		svc := NewService(cfg, stub, nil, serviceTestNow)

		got, err := svc.Read(context.Background(), serviceTestCaller(), serviceTestScope, documentMemoryMD, 0, 0)
		if err != nil {
			t.Fatalf("Read error = %v, want nil", err)
		}
		if len(got) > cfg.MaxReadBytes {
			t.Fatalf("len(Read) = %d, want at most MaxReadBytes %d", len(got), cfg.MaxReadBytes)
		}
		if !strings.HasSuffix(got, "…[truncated]\n") {
			t.Fatalf("Read = %q, want the final …[truncated] line", got)
		}
		if strings.Contains(got, "sk-ABC") {
			t.Fatalf("Read = %q leaks a fragment of the key", got)
		}
	})

	t.Run("a boundary the cut creates is redacted again", func(t *testing.T) {
		stub := newServiceStoreStub()
		stub.documents = Documents{
			MemoryMD: strings.Repeat("a", 146) + " password: supersecret" + strings.Repeat("b", 80) + "\n",
		}
		cfg := serviceTestConfig()
		cfg.MaxReadBytes = 200
		svc := NewService(cfg, stub, nil, serviceTestNow)

		got, err := svc.Read(context.Background(), serviceTestCaller(), serviceTestScope, documentMemoryMD, 0, 0)
		if err != nil {
			t.Fatalf("Read error = %v, want nil", err)
		}
		if !strings.Contains(got, "password: [REDACTED_SECRET]") {
			t.Fatalf("Read = %q, want the repaired redaction marker at the cut", got)
		}
		if strings.Contains(got, "supersecret") {
			t.Fatalf("Read = %q leaks the secret", got)
		}
	})
}

// TestServiceSearch pins design §11.1's case-insensitive search, its document
// order, its result cap and its query validation.
func TestServiceSearch(t *testing.T) {
	needleHits := []SearchHit{
		{Path: documentMemoryMD, Line: 4, Text: "needle here"},
		{Path: documentMemorySummary, Line: 2, Text: "a summary needle"},
		{Path: "rollout_summaries/" + serviceTestAlphaSummary, Line: 1, Text: "rollout needle one"},
		{Path: "rollout_summaries/" + serviceTestBravoSummary, Line: 1, Text: "rollout needle two"},
	}
	cases := []struct {
		name       string
		query      string
		maxResults int
		want       []SearchHit
		wantErr    error
		wantReads  int
	}{
		{
			name:       "case insensitive across documents in path order",
			query:      "NEEDLE",
			maxResults: defaultMaxSearchResults,
			want:       needleHits,
			wantReads:  1,
		},
		{
			name:       "lowercase query matches uppercase text in line order",
			query:      "alpha",
			maxResults: defaultMaxSearchResults,
			want: []SearchHit{
				{Path: documentMemoryMD, Line: 1, Text: "alpha first"},
				{Path: documentMemoryMD, Line: 2, Text: "second ALPHA line"},
			},
			wantReads: 1,
		},
		{
			name:       "trimmed query",
			query:      " \t needle \n",
			maxResults: defaultMaxSearchResults,
			want:       needleHits,
			wantReads:  1,
		},
		{
			name:       "stops at MaxSearchResults",
			query:      "needle",
			maxResults: 2,
			want:       needleHits[:2],
			wantReads:  1,
		},
		{
			name:       "no matches",
			query:      "absent",
			maxResults: defaultMaxSearchResults,
			want:       nil,
			wantReads:  1,
		},
		{
			name:       "query of exactly 256 bytes is accepted",
			query:      strings.Repeat("n", 256),
			maxResults: defaultMaxSearchResults,
			want:       []SearchHit{{Path: documentMemoryMD, Line: 5, Text: strings.Repeat("n", 300)}},
			wantReads:  1,
		},
		{name: "empty query", query: "", maxResults: defaultMaxSearchResults, wantErr: ErrInvalid},
		{name: "blank query", query: " \t\n ", maxResults: defaultMaxSearchResults, wantErr: ErrInvalid},
		{name: "query over 256 bytes", query: strings.Repeat("n", 257), maxResults: defaultMaxSearchResults, wantErr: ErrInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := newServiceStoreStub()
			stub.documents = serviceTestDocuments()
			cfg := serviceTestConfig()
			cfg.MaxSearchResults = tc.maxResults
			svc := NewService(cfg, stub, nil, serviceTestNow)

			got, err := svc.Search(context.Background(), serviceTestCaller(), serviceTestScope, tc.query)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Search(%q) error = %v, want %v", tc.query, err, tc.wantErr)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("Search(%q) returned %d hits, want %d: %+v", tc.query, len(got), len(tc.want), got)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("Search(%q) hit %d = %+v, want %+v", tc.query, i, got[i], tc.want[i])
				}
			}
			if got := stub.documentsCalls(); got != tc.wantReads {
				t.Fatalf("Documents calls = %d, want %d", got, tc.wantReads)
			}
			if tc.wantReads > 0 {
				assertServiceOwners(t, stub.owners, serviceTestOwner())
			}
		})
	}
}

// TestServiceSearchCutAndRedaction pins the 400-byte hit cut and its
// Requirement 3.3 wrap.
func TestServiceSearchCutAndRedaction(t *testing.T) {
	t.Run("hit text is cut to 400 bytes", func(t *testing.T) {
		stub := newServiceStoreStub()
		stub.documents = Documents{MemoryMD: "needle " + strings.Repeat("x", 500) + " tail-marker\n"}
		svc := NewService(serviceTestConfig(), stub, nil, serviceTestNow)

		got, err := svc.Search(context.Background(), serviceTestCaller(), serviceTestScope, "needle")
		if err != nil {
			t.Fatalf("Search error = %v, want nil", err)
		}
		if len(got) != 1 {
			t.Fatalf("Search returned %d hits, want 1", len(got))
		}
		if len(got[0].Text) > 400 {
			t.Fatalf("len(hit.Text) = %d, want at most 400", len(got[0].Text))
		}
		if !strings.HasPrefix(got[0].Text, "needle ") {
			t.Fatalf("hit.Text = %q, want the matching line prefix", got[0].Text)
		}
		if strings.Contains(got[0].Text, "tail-marker") {
			t.Fatalf("hit.Text = %q contains content beyond the cut", got[0].Text)
		}
	})

	t.Run("a secret straddling the cut is redacted", func(t *testing.T) {
		stub := newServiceStoreStub()
		stub.documents = Documents{
			MemoryMD: "needle " + strings.Repeat("a", 370) + "sk-ABCDEFGHIJKLMNOPQRSTUVWX" + strings.Repeat("b", 100) + "\n",
		}
		svc := NewService(serviceTestConfig(), stub, nil, serviceTestNow)

		got, err := svc.Search(context.Background(), serviceTestCaller(), serviceTestScope, "needle")
		if err != nil {
			t.Fatalf("Search error = %v, want nil", err)
		}
		if len(got) != 1 {
			t.Fatalf("Search returned %d hits, want 1", len(got))
		}
		if strings.Contains(got[0].Text, "sk-ABC") {
			t.Fatalf("hit.Text = %q leaks a fragment of the key", got[0].Text)
		}
		if !strings.Contains(got[0].Text, strings.Repeat("a", 370)) {
			t.Fatalf("hit.Text = %q lost content before the secret", got[0].Text)
		}
		if len(got[0].Text) > 400 {
			t.Fatalf("len(hit.Text) = %d, want at most 400", len(got[0].Text))
		}
	})
}

// TestServicePaths pins the fixed document order: MEMORY.md, memory_summary.md
// and the rollout summaries by name.
func TestServicePaths(t *testing.T) {
	stub := newServiceStoreStub()
	stub.documents = serviceTestDocuments()
	svc := NewService(serviceTestConfig(), stub, nil, serviceTestNow)

	got, err := svc.Paths(context.Background(), serviceTestCaller(), serviceTestScope)
	if err != nil {
		t.Fatalf("Paths error = %v, want nil", err)
	}
	want := []string{
		documentMemoryMD,
		documentMemorySummary,
		"rollout_summaries/" + serviceTestAlphaSummary,
		"rollout_summaries/" + serviceTestBravoSummary,
		"rollout_summaries/" + serviceTestCharlieSummary,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Paths = %v, want %v", got, want)
	}
	assertServiceOwners(t, stub.owners, serviceTestOwner())

	t.Run("store errors propagate", func(t *testing.T) {
		stub := newServiceStoreStub()
		stub.documentsErr = ErrNotFound
		svc := NewService(serviceTestConfig(), stub, nil, serviceTestNow)
		if _, err := svc.Paths(context.Background(), serviceTestCaller(), serviceTestScope); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Paths error = %v, want ErrNotFound", err)
		}
	})
}

// TestServiceStatus pins the status-plus-summary read (Requirement 8.2).
func TestServiceStatus(t *testing.T) {
	status := BankStatus{
		ScopeID:         serviceTestScope,
		ProjectLabel:    "/w/service",
		Version:         3,
		PublishedAtMS:   serviceTestTime.UnixMilli(),
		PendingSessions: 1,
		Extractions:     2,
		PendingNotes:    4,
	}
	t.Run("status and published summary", func(t *testing.T) {
		stub := newServiceStoreStub()
		stub.bankStatus = status
		stub.publishedSummary = "the published summary"
		stub.publishedVersion = 3
		svc := NewService(serviceTestConfig(), stub, nil, serviceTestNow)

		gotStatus, gotSummary, err := svc.Status(context.Background(), serviceTestCaller(), serviceTestScope)
		if err != nil {
			t.Fatalf("Status error = %v, want nil", err)
		}
		if gotStatus != status {
			t.Fatalf("Status = %+v, want %+v", gotStatus, status)
		}
		if gotSummary != "the published summary" {
			t.Fatalf("summary = %q, want %q", gotSummary, "the published summary")
		}
		assertServiceOwners(t, stub.owners, serviceTestOwner())
	})

	t.Run("an empty published summary is not a missing bank", func(t *testing.T) {
		stub := newServiceStoreStub()
		stub.bankStatus = status
		stub.publishedSummaryErr = ErrNotFound
		svc := NewService(serviceTestConfig(), stub, nil, serviceTestNow)

		gotStatus, gotSummary, err := svc.Status(context.Background(), serviceTestCaller(), serviceTestScope)
		if err != nil {
			t.Fatalf("Status error = %v, want nil", err)
		}
		if gotStatus != status || gotSummary != "" {
			t.Fatalf("Status = %+v, %q, want the status and an empty summary", gotStatus, gotSummary)
		}
	})

	t.Run("a missing bank reads no summary", func(t *testing.T) {
		stub := newServiceStoreStub()
		stub.bankStatusErr = ErrNotFound
		svc := NewService(serviceTestConfig(), stub, nil, serviceTestNow)

		if _, _, err := svc.Status(context.Background(), serviceTestCaller(), serviceTestScope); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Status error = %v, want ErrNotFound", err)
		}
		if want := []string{"BankStatus"}; !slices.Equal(stub.order, want) {
			t.Fatalf("store calls = %v, want %v", stub.order, want)
		}
	})

	t.Run("store errors propagate", func(t *testing.T) {
		cause := errors.New("store down")
		bankStub := newServiceStoreStub()
		bankStub.bankStatusErr = cause
		svc := NewService(serviceTestConfig(), bankStub, nil, serviceTestNow)
		if _, _, err := svc.Status(context.Background(), serviceTestCaller(), serviceTestScope); !errors.Is(err, cause) {
			t.Fatalf("Status error = %v, want %v", err, cause)
		}

		summaryStub := newServiceStoreStub()
		summaryStub.bankStatus = status
		summaryStub.publishedSummaryErr = cause
		svc = NewService(serviceTestConfig(), summaryStub, nil, serviceTestNow)
		if _, _, err := svc.Status(context.Background(), serviceTestCaller(), serviceTestScope); !errors.Is(err, cause) {
			t.Fatalf("Status error = %v, want %v", err, cause)
		}
	})
}

// TestServiceList pins that the caller's bank list passes through for the
// caller principal (Requirement 8.1).
func TestServiceList(t *testing.T) {
	t.Run("the caller's banks pass through", func(t *testing.T) {
		stub := newServiceStoreStub()
		stub.listBanks = []BankStatus{{ScopeID: serviceTestScope, Version: 4}}
		svc := NewService(serviceTestConfig(), stub, nil, serviceTestNow)

		got, err := svc.List(context.Background(), serviceTestCaller())
		if err != nil {
			t.Fatalf("List error = %v, want nil", err)
		}
		if len(got) != len(stub.listBanks) || got[0] != stub.listBanks[0] {
			t.Fatalf("List = %+v, want %+v", got, stub.listBanks)
		}
		if want := []Principal{serviceTestCaller()}; !slices.Equal(stub.listBanksPrincipals, want) {
			t.Fatalf("ListBanks principals = %+v, want %+v", stub.listBanksPrincipals, want)
		}
	})

	t.Run("store errors propagate", func(t *testing.T) {
		stub := newServiceStoreStub()
		cause := errors.New("store down")
		stub.listBanksErr = cause
		svc := NewService(serviceTestConfig(), stub, nil, serviceTestNow)
		if _, err := svc.List(context.Background(), serviceTestCaller()); !errors.Is(err, cause) {
			t.Fatalf("List error = %v, want %v", err, cause)
		}
	})
}

// TestServiceAddNote pins design §11.1's note validation, redaction and
// identifier (Requirement 7.5).
func TestServiceAddNote(t *testing.T) {
	const noteLimit = 32
	cfg := serviceTestConfig()
	cfg.MaxNoteBytes = noteLimit

	cases := []struct {
		name     string
		text     string
		wantText string
		wantErr  error
	}{
		{name: "stored trimmed", text: "  keep this note  ", wantText: "keep this note"},
		{name: "redacts secrets", text: "token=supersecretvalue123", wantText: "token=[REDACTED_SECRET]"},
		{name: "removes NUL bytes before redacting", text: "keep\x00this", wantText: "keepthis"},
		{name: "at the limit", text: strings.Repeat("a", noteLimit), wantText: strings.Repeat("a", noteLimit)},
		{name: "empty", text: "", wantErr: ErrInvalid},
		{name: "blank", text: " \t\n ", wantErr: ErrInvalid},
		{name: "over the limit", text: strings.Repeat("a", noteLimit+1), wantErr: ErrInvalid},
		{name: "over the limit after trimming", text: " " + strings.Repeat("a", noteLimit+1) + " ", wantErr: ErrInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := newServiceStoreStub()
			svc := NewService(cfg, stub, nil, serviceTestNow)

			err := svc.AddNote(context.Background(), serviceTestCaller(), serviceTestScope, tc.text)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("AddNote(%q) error = %v, want %v", tc.text, err, tc.wantErr)
			}
			if tc.wantErr != nil {
				if len(stub.order) != 0 {
					t.Fatalf("store calls = %v, want none for a rejected note", stub.order)
				}
				return
			}
			if len(stub.addedNotes) != 1 {
				t.Fatalf("AddNote stored %d notes, want 1", len(stub.addedNotes))
			}
			note := stub.addedNotes[0]
			if note.Text != tc.wantText {
				t.Fatalf("note text = %q, want %q", note.Text, tc.wantText)
			}
			if !serviceNoteIDPattern.MatchString(note.NoteID) {
				t.Fatalf("note id = %q, want 32 lowercase hex characters", note.NoteID)
			}
			if note.CreatedAtMS != serviceTestTime.UnixMilli() {
				t.Fatalf("note created_at = %d, want %d", note.CreatedAtMS, serviceTestTime.UnixMilli())
			}
			assertServiceOwners(t, stub.addedOwners, serviceTestOwner())
		})
	}

	t.Run("two notes get different identifiers", func(t *testing.T) {
		stub := newServiceStoreStub()
		svc := NewService(cfg, stub, nil, serviceTestNow)
		ctx := context.Background()
		if err := svc.AddNote(ctx, serviceTestCaller(), serviceTestScope, "one"); err != nil {
			t.Fatalf("AddNote error = %v, want nil", err)
		}
		if err := svc.AddNote(ctx, serviceTestCaller(), serviceTestScope, "two"); err != nil {
			t.Fatalf("AddNote error = %v, want nil", err)
		}
		if len(stub.addedNotes) != 2 {
			t.Fatalf("AddNote stored %d notes, want 2", len(stub.addedNotes))
		}
		if stub.addedNotes[0].NoteID == stub.addedNotes[1].NoteID {
			t.Fatalf("note ids are equal: %q", stub.addedNotes[0].NoteID)
		}
	})
}

// TestServiceForget pins that Forget carries the caller's owner, passes the
// control clock and counts control_forget (Requirements 8.3, 8.6).
func TestServiceForget(t *testing.T) {
	t.Run("counts control_forget", func(t *testing.T) {
		stub := newServiceStoreStub()
		metrics := NewPrometheusCollector()
		svc := NewService(serviceTestConfig(), stub, metrics, serviceTestNow)

		if err := svc.Forget(context.Background(), serviceTestCaller(), serviceTestScope); err != nil {
			t.Fatalf("Forget error = %v, want nil", err)
		}
		assertServiceOwners(t, stub.forgetOwners, serviceTestOwner())
		if want := []int64{serviceTestTime.UnixMilli()}; !slices.Equal(stub.forgetNowMS, want) {
			t.Fatalf("Forget nowMS = %v, want %v", stub.forgetNowMS, want)
		}
		if got := testutil.ToFloat64(metrics.events.WithLabelValues(EventControlForget)); got != 1 {
			t.Fatalf("aip_proxy_memory_events_total{event=%q} = %v, want 1", EventControlForget, got)
		}
	})

	t.Run("a store error is not counted", func(t *testing.T) {
		stub := newServiceStoreStub()
		stub.forgetErr = ErrNotFound
		metrics := NewPrometheusCollector()
		svc := NewService(serviceTestConfig(), stub, metrics, serviceTestNow)

		if err := svc.Forget(context.Background(), serviceTestCaller(), serviceTestScope); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Forget error = %v, want ErrNotFound", err)
		}
		if got := testutil.ToFloat64(metrics.events.WithLabelValues(EventControlForget)); got != 0 {
			t.Fatalf("aip_proxy_memory_events_total{event=%q} = %v, want 0", EventControlForget, got)
		}
	})
}

// TestServiceStoreOwners pins that every scoped store call receives the
// caller's owner triple, never a default or another principal's values.
func TestServiceStoreOwners(t *testing.T) {
	stub := newServiceStoreStub()
	stub.documents = serviceTestDocuments()
	stub.bankStatus = BankStatus{ScopeID: serviceTestAltScope}
	stub.publishedSummary = "summary"
	stub.listBanks = []BankStatus{{ScopeID: serviceTestAltScope}}
	svc := NewService(serviceTestConfig(), stub, nil, serviceTestNow)

	caller := Principal{TenantID: "tenant-service-alt", PrincipalID: "carol-service"}
	ctx := context.Background()
	// The scoped methods trust the already eligibility-checked Principal; this
	// test passes a different principal than the fixture to prove the owner is
	// built from the caller, not from any default.
	if _, _, err := svc.Status(ctx, caller, serviceTestAltScope); err != nil {
		t.Fatalf("Status error = %v, want nil", err)
	}
	if _, err := svc.Paths(ctx, caller, serviceTestAltScope); err != nil {
		t.Fatalf("Paths error = %v, want nil", err)
	}
	if _, err := svc.Read(ctx, caller, serviceTestAltScope, documentMemoryMD, 0, 0); err != nil {
		t.Fatalf("Read error = %v, want nil", err)
	}
	if _, err := svc.Search(ctx, caller, serviceTestAltScope, "needle"); err != nil {
		t.Fatalf("Search error = %v, want nil", err)
	}
	if err := svc.AddNote(ctx, caller, serviceTestAltScope, "note"); err != nil {
		t.Fatalf("AddNote error = %v, want nil", err)
	}
	if err := svc.Forget(ctx, caller, serviceTestAltScope); err != nil {
		t.Fatalf("Forget error = %v, want nil", err)
	}
	if _, err := svc.List(ctx, caller); err != nil {
		t.Fatalf("List error = %v, want nil", err)
	}

	want := Owner{TenantID: caller.TenantID, PrincipalID: caller.PrincipalID, ProjectKey: serviceTestAltScope}
	// Status reads two, every other scoped method reads one.
	if got, wantOwners := len(stub.owners), 7; got != wantOwners {
		t.Fatalf("recorded owners = %d, want %d", got, wantOwners)
	}
	assertServiceOwners(t, stub.owners, want)
	if wantPrincipals := []Principal{caller}; !slices.Equal(stub.listBanksPrincipals, wantPrincipals) {
		t.Fatalf("ListBanks principals = %+v, want %+v", stub.listBanksPrincipals, wantPrincipals)
	}
}

// serviceValidatedReadConfig builds a valid configuration with the requested
// MaxReadBytes, matching the panel probe's DefaultConfig-plus-required-fields
// shape.
func serviceValidatedReadConfig(limit int) Config {
	cfg := DefaultConfig()
	cfg.Principals = []string{serviceTestPrincipal}
	cfg.ExtractRoute = "route-service-extract"
	cfg.ConsolidateRoute = "route-service-consolidate"
	cfg.MaxReadBytes = limit
	return cfg
}

// TestServiceConfigMinReadBytesLimit pins the MaxReadBytes configuration floor
// (design §11.1, Requirement 7.3): a read limited below the marker floor cannot
// deliver the promised final "…[truncated]" line within its own cap, so a
// positive limit under it is rejected at validation time instead of silently
// producing an over-limit response. Zero keeps its existing rejection — every
// limit is positive (design §1.2) — and only the 1-15 window gets the
// floor-specific error.
func TestServiceConfigMinReadBytesLimit(t *testing.T) {
	// The floor is the separator newline that makes the marker its own line
	// plus the "…[truncated]\n" line itself.
	const wantFloor = 16
	if floor := 1 + len(readTruncatedLine); floor != wantFloor {
		t.Fatalf("marker floor = %d, want %d", floor, wantFloor)
	}
	floorErr := "proxy-memory: max_read_bytes: must be at least 16 to fit the truncation marker line"
	positiveErr := "proxy-memory: max_read_bytes: must be a positive integer"
	cases := []struct {
		name    string
		limit   int
		wantErr string
	}{
		{name: "zero is not positive", limit: 0, wantErr: positiveErr},
		{name: "one byte", limit: 1, wantErr: floorErr},
		{name: "fourteen bytes", limit: 14, wantErr: floorErr},
		{name: "one byte below the floor", limit: 15, wantErr: floorErr},
		{name: "the floor itself", limit: 16},
		{name: "the floor plus one", limit: 17},
		{name: "an ordinary limit", limit: 32},
		{name: "a large limit", limit: 100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := serviceValidatedReadConfig(tc.limit)

			err := cfg.Validate()
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("Validate(max_read_bytes=%d) error = %v, want nil", tc.limit, err)
			case tc.wantErr != "" && err == nil:
				t.Fatalf("Validate(max_read_bytes=%d) error = nil, want %q", tc.limit, tc.wantErr)
			case tc.wantErr != "" && err.Error() != tc.wantErr:
				t.Fatalf("Validate(max_read_bytes=%d) error = %q, want %q", tc.limit, err, tc.wantErr)
			}
		})
	}
}

// TestServiceValidatedSmallReadLimit mirrors the panel probe with the
// marker-floor policy: 1, 14 and 15 are rejected at validation time, and every
// validated small limit (16, 17, 32) still yields a read no longer than
// MaxReadBytes. A direct-library config that bypasses Validate must stay
// bounded too, without a marker that cannot fit.
func TestServiceValidatedSmallReadLimit(t *testing.T) {
	doc := strings.Repeat("a", 100)
	cases := []struct {
		name    string
		limit   int
		wantErr bool
	}{
		{name: "one byte", limit: 1, wantErr: true},
		{name: "fourteen bytes", limit: 14, wantErr: true},
		{name: "one byte below the floor", limit: 15, wantErr: true},
		{name: "the floor itself", limit: 16},
		{name: "the floor plus one", limit: 17},
		{name: "an ordinary small limit", limit: 32},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := serviceValidatedReadConfig(tc.limit)

			err := cfg.Validate()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Validate(max_read_bytes=%d) error = nil, want the marker-floor rejection", tc.limit)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate(max_read_bytes=%d) error = %v, want nil", tc.limit, err)
			}
			stub := newServiceStoreStub()
			stub.documents = Documents{MemoryMD: doc}
			svc := NewService(cfg, stub, nil, serviceTestNow)

			got, err := svc.Read(context.Background(), serviceTestCaller(), serviceTestScope, documentMemoryMD, 0, 0)
			if err != nil {
				t.Fatalf("Read error = %v, want nil", err)
			}
			if len(got) > tc.limit {
				t.Fatalf("validated MaxReadBytes=%d, Read returned %d bytes", tc.limit, len(got))
			}
		})
	}

	t.Run("the floor carries exactly the marker line", func(t *testing.T) {
		cfg := serviceValidatedReadConfig(16)
		stub := newServiceStoreStub()
		stub.documents = Documents{MemoryMD: doc}
		svc := NewService(cfg, stub, nil, serviceTestNow)

		got, err := svc.Read(context.Background(), serviceTestCaller(), serviceTestScope, documentMemoryMD, 0, 0)
		if err != nil {
			t.Fatalf("Read error = %v, want nil", err)
		}
		if want := "\n" + readTruncatedLine; got != want {
			t.Fatalf("Read = %q, want %q", got, want)
		}
		if len(got) != 16 {
			t.Fatalf("len(Read) = %d, want 16", len(got))
		}
	})

	t.Run("a large limit is unchanged", func(t *testing.T) {
		cfg := serviceValidatedReadConfig(1024)
		stub := newServiceStoreStub()
		stub.documents = Documents{MemoryMD: doc}
		svc := NewService(cfg, stub, nil, serviceTestNow)

		got, err := svc.Read(context.Background(), serviceTestCaller(), serviceTestScope, documentMemoryMD, 0, 0)
		if err != nil {
			t.Fatalf("Read error = %v, want nil", err)
		}
		if want := "1: " + doc + "\n"; got != want {
			t.Fatalf("Read = %q, want %q", got, want)
		}
	})

	t.Run("a config that bypasses Validate stays bounded", func(t *testing.T) {
		for _, limit := range []int{1, 8, 15} {
			cfg := serviceTestConfig()
			cfg.MaxReadBytes = limit
			stub := newServiceStoreStub()
			stub.documents = Documents{MemoryMD: doc}
			svc := NewService(cfg, stub, nil, serviceTestNow)

			got, err := svc.Read(context.Background(), serviceTestCaller(), serviceTestScope, documentMemoryMD, 0, 0)
			if err != nil {
				t.Fatalf("Read error = %v, want nil", err)
			}
			if len(got) > limit {
				t.Fatalf("unvalidated MaxReadBytes=%d, Read returned %d bytes", limit, len(got))
			}
			if strings.Contains(got, "truncated") {
				t.Fatalf("unvalidated MaxReadBytes=%d, Read = %q carries a marker that cannot fit", limit, got)
			}
		}
	})
}
