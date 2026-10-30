package journalstore_test

// Task 8.1 remediation Finding 1: real durable round-trip for every
// multimodal direction vector through the supported SQLite journalstore
// persistence API. Observations are appended through AppendObservations,
// the file-backed store is closed and reopened, and each vector is read
// back through the consumer-facing GetObservation / ListObservations /
// ListObservationComponents APIs. Test-only; production migrations,
// constructors and queries are reused unchanged.

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	_ "modernc.org/sqlite"
)

const j81StoreID = "ref81-journal"

type j81Vector struct {
	name            string
	component       string
	direction       metering.FlowDirection
	unit            string
	dimensions      []metering.Dimension
	supplierQty     string
	customerQty     string
	backendBoundary metering.Boundary
}

func j81Vectors() []j81Vector {
	return []j81Vector{
		{
			name: "image-input", component: metering.ComponentImage, direction: metering.DirectionInput,
			unit:            metering.UnitImage,
			dimensions:      []metering.Dimension{{Name: "quality", Value: "hd"}, {Name: "transform", Value: "resize"}},
			supplierQty:     "2",
			customerQty:     "1",
			backendBoundary: metering.BoundaryBackendEgress,
		},
		{
			name: "image-output", component: metering.ComponentImage, direction: metering.DirectionOutput,
			unit:            metering.UnitImage,
			dimensions:      []metering.Dimension{{Name: "quality", Value: "hd"}, {Name: "transform", Value: "transcode"}},
			supplierQty:     "3",
			customerQty:     "1",
			backendBoundary: metering.BoundaryBackendIngress,
		},
		{
			name: "audio-input", component: metering.ComponentAudio, direction: metering.DirectionInput,
			unit:            metering.UnitSecond,
			dimensions:      []metering.Dimension{{Name: "codec", Value: "pcm"}, {Name: "transform", Value: "trim"}},
			supplierQty:     "12.5",
			customerQty:     "10.0",
			backendBoundary: metering.BoundaryBackendEgress,
		},
		{
			name: "audio-output", component: metering.ComponentAudio, direction: metering.DirectionOutput,
			unit:            metering.UnitSecond,
			dimensions:      []metering.Dimension{{Name: "codec", Value: "opus"}, {Name: "transform", Value: "resample"}},
			supplierQty:     "8.0",
			customerQty:     "7.5",
			backendBoundary: metering.BoundaryBackendIngress,
		},
		{
			name: "video-input", component: metering.ComponentVideo, direction: metering.DirectionInput,
			unit:            metering.UnitToken,
			dimensions:      []metering.Dimension{{Name: "tokenizer", Value: "provider_native"}},
			supplierQty:     "4096",
			customerQty:     "3072",
			backendBoundary: metering.BoundaryBackendEgress,
		},
		{
			name: "video-output", component: metering.ComponentVideo, direction: metering.DirectionOutput,
			unit:            metering.UnitSecond,
			dimensions:      []metering.Dimension{{Name: "generation", Value: "provider_generated"}},
			supplierQty:     "8",
			customerQty:     "7",
			backendBoundary: metering.BoundaryBackendIngress,
		},
		{
			name: "document-input", component: metering.ComponentDocument, direction: metering.DirectionInput,
			unit:            metering.UnitPage,
			dimensions:      []metering.Dimension{{Name: "format", Value: "pdf"}, {Name: "transform", Value: "extract"}},
			supplierQty:     "4",
			customerQty:     "3",
			backendBoundary: metering.BoundaryBackendEgress,
		},
		{
			name: "document-output", component: metering.ComponentDocument, direction: metering.DirectionOutput,
			unit:            metering.UnitPage,
			dimensions:      []metering.Dimension{{Name: "format", Value: "pdf"}, {Name: "transform", Value: "paginate"}},
			supplierQty:     "2",
			customerQty:     "1",
			backendBoundary: metering.BoundaryBackendIngress,
		},
	}
}

func j81Key(v j81Vector) metering.ComponentKey {
	return metering.ComponentKey{
		Direction:  v.direction,
		Component:  v.component,
		Unit:       v.unit,
		SchemaID:   "refinement81.cert.v1",
		Dimensions: append([]metering.Dimension(nil), v.dimensions...),
	}
}

func j81Decimal(t *testing.T, raw string) metering.Decimal {
	t.Helper()
	d, err := metering.ParseDecimal(raw)
	if err != nil {
		t.Fatalf("ParseDecimal(%q): %v", raw, err)
	}
	n, err := d.Normalize()
	if err != nil {
		t.Fatalf("Normalize(%q): %v", raw, err)
	}
	return n
}

// j81Observation builds one neutral V2 B-leg observation. Supplier rows are
// provider-origin at backend boundaries; customer rows are local-origin at
// frontend boundaries with a deliberately different quantity.
func j81Observation(t *testing.T, id, bLegID, origin string, boundary metering.Boundary, perspective metering.EconomicPerspective, key metering.ComponentKey, quantity string) metering.Observation {
	t.Helper()
	acquisition := metering.AcquisitionLocalTransport
	if origin == metering.OriginProvider {
		acquisition = metering.AcquisitionProviderResponse
	}
	d := j81Decimal(t, quantity)
	now := time.Unix(1_700_000_081, 0).UTC()
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: id, SourceEventKey: id + "-event",
		Revision: 1, StreamID: id + "-stream", Sequence: 1,
		Origin: origin, Acquisition: acquisition, Authority: metering.AuthorityObservedClaim,
		Perspective: perspective, Boundary: boundary, Lifecycle: metering.LifecycleBackendAttempt,
		Subject: metering.SubjectRef{
			Kind: metering.SubjectBLeg, StoreID: j81StoreID,
			ALegID: "a-81", BillingCallID: "call-81", BLegID: bLegID,
		},
		Correlation: metering.CorrelationV2{
			StoreID: j81StoreID, ALegID: "a-81", BillingCallID: "call-81", BLegID: bLegID,
		},
		Semantics:  metering.SemanticsDelta,
		ObservedAt: now, ReceivedAt: now, MappingRef: "refinement81.cert.v1",
		Measures: []metering.Measure{{
			Key: key, Value: &d, Quality: metering.QualityObserved,
			MethodRef: "refinement81.cert.v1",
		}},
	}
}

//nolint:revive // test helper keeps t first per Go testing convention
func j81OpenFileStore(t *testing.T, ctx context.Context, path string) *journalstore.DurableStore {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	store, err := journalstore.NewDurableStore(ctx, bunDB, journalstore.DurableConfig{StoreID: j81StoreID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func j81CustomerBoundary(boundary metering.Boundary) metering.Boundary {
	if boundary == metering.BoundaryBackendEgress {
		return metering.BoundaryFrontendIngress
	}
	return metering.BoundaryFrontendEgress
}

// TestRefinement81_JournalDurability appends every 6.1 modality-direction
// vector (plus the distinct customer-boundary representation for both 6.2
// transform directions) through the real SQLite journalstore API, reopens
// the file-backed store, and proves exact readback through the
// consumer-facing query APIs.
func TestRefinement81_JournalDurability(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ref81-journal.db")
	store := j81OpenFileStore(t, ctx, path)

	type appended struct {
		vector      j81Vector
		key         metering.ComponentKey
		supplier    metering.Observation
		customer    metering.Observation
		fingerprint string
	}
	var rows []appended
	var batch []metering.Observation
	for _, v := range j81Vectors() {
		key := j81Key(v)
		if err := key.Validate(); err != nil {
			t.Fatalf("%s key: %v", v.name, err)
		}
		bLeg := "b-81-j-" + v.name
		supplier := j81Observation(t, "ref81-"+v.name+"-supplier", bLeg,
			metering.OriginProvider, v.backendBoundary, metering.PerspectiveOperator, key, v.supplierQty)
		customer := j81Observation(t, "ref81-"+v.name+"-customer", bLeg,
			metering.OriginLocal, j81CustomerBoundary(v.backendBoundary), metering.PerspectiveCustomer, key, v.customerQty)
		for _, obs := range []metering.Observation{supplier, customer} {
			if err := obs.Validate(); err != nil {
				t.Fatalf("%s validate: %v", obs.ID, err)
			}
		}
		rows = append(rows, appended{vector: v, key: key, supplier: supplier, customer: customer, fingerprint: supplier.Fingerprint()})
		batch = append(batch, supplier, customer)
	}
	if err := store.AppendObservations(ctx, batch); err != nil {
		t.Fatalf("AppendObservations: %v", err)
	}
	// Exact replay through the same API must stay idempotent.
	if err := store.AppendObservations(ctx, batch); err != nil {
		t.Fatalf("replay AppendObservations: %v", err)
	}

	// Reopen from durable file state: a new store handle over the same
	// path, so no in-memory instance is reused for readback.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := j81OpenFileStore(t, ctx, path)

	for _, row := range rows {
		t.Run(row.vector.name, func(t *testing.T) {
			for _, want := range []struct {
				obs      metering.Observation
				origin   string
				boundary metering.Boundary
				qty      string
			}{
				{row.supplier, metering.OriginProvider, row.vector.backendBoundary, row.vector.supplierQty},
				{row.customer, metering.OriginLocal, j81CustomerBoundary(row.vector.backendBoundary), row.vector.customerQty},
			} {
				got, err := reopened.GetObservation(ctx, want.obs.ID, 1)
				if err != nil {
					t.Fatalf("GetObservation(%q): %v", want.obs.ID, err)
				}
				if err := got.Validate(); err != nil {
					t.Fatalf("readback Validate: %v", err)
				}
				if got.Fingerprint() != want.obs.Fingerprint() {
					t.Fatalf("%s fingerprint drift across durable reopen", want.obs.ID)
				}
				if got.Subject.StoreID != j81StoreID || got.Subject.BillingCallID != "call-81" ||
					got.Subject.ALegID != "a-81" || got.Subject.BLegID != "b-81-j-"+row.vector.name ||
					got.Subject.Kind != metering.SubjectBLeg {
					t.Fatalf("%s subject=%+v", want.obs.ID, got.Subject)
				}
				if got.Origin != want.origin || got.Boundary != want.boundary ||
					got.Acquisition != want.obs.Acquisition || got.Authority != metering.AuthorityObservedClaim ||
					got.Perspective != want.obs.Perspective || got.Lifecycle != metering.LifecycleBackendAttempt {
					t.Fatalf("%s provenance=%s/%s/%s/%s/%s", want.obs.ID,
						got.Origin, got.Boundary, got.Acquisition, got.Authority, got.Perspective)
				}
				if len(got.Measures) != 1 || !got.Measures[0].Key.Equal(row.key) {
					t.Fatalf("%s measures=%+v, want exact certified key", want.obs.ID, got.Measures)
				}
				mk := got.Measures[0].Key
				if mk.Direction != row.vector.direction || mk.Component != row.vector.component ||
					mk.Unit != row.vector.unit || mk.SchemaID != "refinement81.cert.v1" {
					t.Fatalf("%s key=%+v", want.obs.ID, mk)
				}
				if len(mk.Dimensions) != len(row.vector.dimensions) {
					t.Fatalf("%s dimensions=%+v", want.obs.ID, mk.Dimensions)
				}
				if got.Measures[0].Value == nil ||
					got.Measures[0].Value.CanonicalString() != j81Decimal(t, want.qty).CanonicalString() {
					t.Fatalf("%s quantity=%+v, want %q", want.obs.ID, got.Measures[0].Value, want.qty)
				}
				if got.Measures[0].Quality != metering.QualityObserved {
					t.Fatalf("%s quality=%q", want.obs.ID, got.Measures[0].Quality)
				}

				// Consumer-facing stream query must return the same envelope.
				page, err := reopened.ListObservations(ctx, journalstore.ObservationQuery{StreamID: want.obs.StreamID, Limit: 10})
				if err != nil {
					t.Fatalf("ListObservations: %v", err)
				}
				if len(page.Observations) != 1 || page.Observations[0].Fingerprint() != want.obs.Fingerprint() {
					t.Fatalf("ListObservations returned %d rows, want the exact envelope", len(page.Observations))
				}

				// Component projection must preserve the native unit,
				// exact decimal parts and provider/customer source.
				comps, err := reopened.ListObservationComponents(ctx, journalstore.ComponentQuery{
					StreamID: want.obs.StreamID, ComponentCanonicalKey: row.key.CanonicalKey(), Limit: 10,
				})
				if err != nil {
					t.Fatalf("ListObservationComponents: %v", err)
				}
				if len(comps.Components) != 1 {
					t.Fatalf("component rows=%d, want 1", len(comps.Components))
				}
				proj := comps.Components[0]
				wantDec := j81Decimal(t, want.qty)
				if proj.Coefficient != wantDec.Coefficient || proj.Scale != wantDec.Scale {
					t.Fatalf("projection quantity=%s/%d, want %s/%d",
						proj.Coefficient, proj.Scale, wantDec.Coefficient, wantDec.Scale)
				}
				if !proj.ValuePresent || proj.ComponentKey != row.key.CanonicalKey() ||
					proj.ComponentKeyHash != row.key.Fingerprint() {
					t.Fatalf("projection identity=%+v", proj)
				}
				if proj.ObservationID != want.obs.ID || proj.ObservationRevision != 1 ||
					proj.ObservationFingerprint != want.obs.Fingerprint() {
					t.Fatalf("projection lineage=%+v", proj)
				}
				if proj.Origin != want.origin || proj.SubjectKind != metering.SubjectBLeg ||
					proj.SubjectID != "b-81-j-"+row.vector.name || proj.StreamID != want.obs.StreamID {
					t.Fatalf("projection source=%+v", proj)
				}
			}
		})
	}
}
