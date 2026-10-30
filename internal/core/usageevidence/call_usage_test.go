package usageevidence

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func testCallUsageRecord(callID BillingCallID) CallUsageRecord {
	return CallUsageRecord{
		SchemaVersion: CurrentRecordSchemaVersion,
		CallID:        callID,
		AccountID:     "acct-1",
		ALegID:        "a-shared",
		SessionID:     "sess-shared",
		StartedAt:     time.Unix(100, 0).UTC(),
		FinishedAt:    time.Unix(101, 0).UTC(),
		Outcome:       TurnOutcomeCompleted,
		CustomerPricingRef: VersionRef{
			ID:      "prices",
			Version: "v1",
		},
		ChargePolicyRef: VersionRef{
			ID:      "policy",
			Version: "v2",
		},
		ExpectedBLegIDs: []string{"b-fail", "b-win"},
	}
}

func testCallLegUsageRecord(callID BillingCallID, bLegID string) CallLegUsageRecord {
	return CallLegUsageRecord{
		CallID:     callID,
		ALegID:     "a-shared",
		BLegID:     bLegID,
		BackendID:  "backend-a",
		ProviderID: "provider-a",
		ModelID:    "model-a",
		StartedAt:  time.Unix(100, 0).UTC(),
		FinishedAt: time.Unix(100, 500000000).UTC(),
		Outcome:    LegOutcomeWinner,
		Surfaced:   SurfacedYes,
		Evidence: FinalBillingEvidence{
			InputTokens:  Quantity{Value: 7, Present: true},
			OutputTokens: Quantity{Value: 3, Present: true},
			Cost:         MoneyEvidence{NanoUnits: 11, Currency: "USD", Present: true},
			Source:       EvidenceSourceProviderReported,
			Authority:    EvidenceAuthorityAuthoritative,
			DedupeKey:    "provider-charge-1",
		},
		OperatorRateRef: VersionRef{ID: "operator-rates", Version: "v4"},
	}
}

func TestCallUsageRecordSealAssignsBillingCallKeyWithoutEmbeddingLegs(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	record, err := testCallUsageRecord(callID).Seal()
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if got, want := record.Key, callID.String(); got != want {
		t.Fatalf("call usage key = %q, want BillingCallID %q", got, want)
	}
	if strings.Contains(record.Key, "a-shared") || strings.Contains(record.Key, "sess-shared") || strings.Contains(record.Key, "acct-1") {
		t.Fatal("call-closure durable key must be BillingCallID, not account/A-leg/session")
	}
	if len(record.Fingerprint) != 64 {
		t.Fatalf("call fingerprint = %q, want SHA-256 hex", record.Fingerprint)
	}
	if record.ExpectedBLegIDs == nil || len(record.ExpectedBLegIDs) != 2 {
		t.Fatalf("expected B-leg IDs = %#v", record.ExpectedBLegIDs)
	}
	typ := reflect.TypeFor[CallUsageRecord]()
	if _, ok := typ.FieldByName("Legs"); ok {
		t.Fatal("CallUsageRecord must not embed leg payloads")
	}
	field, ok := typ.FieldByName("ExpectedBLegIDs")
	if !ok || field.Type.Kind() != reflect.Slice || field.Type.Elem().Kind() != reflect.String {
		t.Fatal("call-closure expected B-leg identities must be ID strings, not leg records")
	}
}

func TestCallLegUsageRecordIsIndependentOfTURAndKeyedByCallPlusBLeg(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	fail, err := testCallLegUsageRecord(callID, "b-fail").Seal()
	if err != nil {
		t.Fatalf("seal fail leg: %v", err)
	}
	win, err := testCallLegUsageRecord(callID, "b-win").Seal()
	if err != nil {
		t.Fatalf("seal win leg: %v", err)
	}
	retry, err := testCallLegUsageRecord(callID, "b-retry").Seal()
	if err != nil {
		t.Fatalf("seal retry leg: %v", err)
	}
	if fail.CallID != callID || win.CallID != callID || retry.CallID != callID {
		t.Fatal("retries, failover alternatives, and parallel B-legs must share the incoming BillingCallID")
	}
	seen := map[string]struct{}{fail.Key: {}, win.Key: {}, retry.Key: {}}
	if len(seen) != 3 {
		t.Fatal("each independent B-leg usage record must be unique for BillingCallID + B-leg")
	}
	wantFail, err := CallLegUsageKey(callID, "b-fail")
	if err != nil {
		t.Fatal(err)
	}
	if fail.Key != wantFail {
		t.Fatalf("leg key = %q, want %q", fail.Key, wantFail)
	}
	if _, ok := reflect.TypeFor[CallLegUsageRecord]().FieldByName("CallID"); !ok {
		t.Fatal("current CallLegUsageRecord must carry BillingCallID for authoritative usage identity")
	}
}

func TestCallUsageReplayIdenticalIsNoopAndConflictIsIntegrityError(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	existing, err := testCallUsageRecord(callID).Seal()
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckCallUsageReplay(existing, existing); err != nil {
		t.Fatalf("identical call replay: %v", err)
	}
	conflictSrc := testCallUsageRecord(callID)
	conflictSrc.Outcome = TurnOutcomeFailed
	conflict, err := conflictSrc.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckCallUsageReplay(existing, conflict); !errors.Is(err, ErrReplayConflict) {
		t.Fatalf("conflicting call replay = %v, want ErrReplayConflict", err)
	}
	other, err := testCallUsageRecord(mustBillingCallID(t)).Seal()
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckCallUsageReplay(existing, other); !errors.Is(err, ErrReplayKeyMismatch) {
		t.Fatalf("mismatched call key = %v, want ErrReplayKeyMismatch", err)
	}
}

func TestCallLegUsageReplayIdenticalIsNoopAndConflictIsIntegrityError(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	existing, err := testCallLegUsageRecord(callID, "b-1").Seal()
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckCallLegUsageReplay(existing, existing); err != nil {
		t.Fatalf("identical leg replay: %v", err)
	}
	conflictSrc := testCallLegUsageRecord(callID, "b-1")
	conflictSrc.ModelID = "different-model"
	conflict, err := conflictSrc.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckCallLegUsageReplay(existing, conflict); !errors.Is(err, ErrReplayConflict) {
		t.Fatalf("conflicting leg replay = %v, want ErrReplayConflict", err)
	}
}

func TestCallUsageFingerprintExcludesStoredKeysAndDoesNotHashLegPayloads(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	base, err := testCallUsageRecord(callID).Seal()
	if err != nil {
		t.Fatal(err)
	}
	mutated := base
	mutated.Key = "database-key"
	mutated.Fingerprint = "database-fingerprint"
	got, err := mutated.SemanticFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if got != base.Fingerprint {
		t.Fatal("stored key/fingerprint must not participate in the semantic hash")
	}
	reordered := testCallUsageRecord(callID)
	reordered.ExpectedBLegIDs = []string{"b-win", "b-fail"}
	sealedReordered, err := reordered.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if sealedReordered.Fingerprint != base.Fingerprint {
		t.Fatal("expected B-leg identities are a set; order must not change the fingerprint")
	}
	if err := CheckCallUsageReplay(base, sealedReordered); err != nil {
		t.Fatalf("same allocated set replay: %v", err)
	}
	if len(sealedReordered.ExpectedBLegIDs) != 2 || sealedReordered.ExpectedBLegIDs[0] != "b-fail" || sealedReordered.ExpectedBLegIDs[1] != "b-win" {
		t.Fatalf("sealed expected B-legs = %#v, want canonical [b-fail b-win]", sealedReordered.ExpectedBLegIDs)
	}
}

func TestCallLegUsageAllowsExplicitEvidenceUnavailable(t *testing.T) {
	t.Parallel()
	src := testCallLegUsageRecord(mustBillingCallID(t), "b-never")
	src.Outcome = LegOutcomeFailed
	src.Surfaced = SurfacedNo
	src.Evidence = FinalBillingEvidence{
		Source:    EvidenceSourceUnavailable,
		Authority: EvidenceAuthorityUnavailable,
	}
	sealed, err := src.Seal()
	if err != nil {
		t.Fatalf("evidence-unavailable must seal: %v", err)
	}
	if sealed.Evidence.Source != EvidenceSourceUnavailable || sealed.Evidence.Authority != EvidenceAuthorityUnavailable {
		t.Fatalf("evidence unavailable not preserved: %+v", sealed.Evidence)
	}
}

func TestCallLegUsageSealAcceptsRejectedNeverStartedAndEvidenceUnavailable(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	cases := []struct {
		name     string
		bLegID   string
		outcome  LegOutcome
		surfaced SurfacedState
		evidence FinalBillingEvidence
	}{
		{
			name:     "rejected",
			bLegID:   "b-rejected",
			outcome:  LegOutcomeRejected,
			surfaced: SurfacedNo,
			evidence: FinalBillingEvidence{
				Source:    EvidenceSourceUnavailable,
				Authority: EvidenceAuthorityUnavailable,
			},
		},
		{
			name:     "never_started",
			bLegID:   "b-never-started",
			outcome:  LegOutcomeNeverStarted,
			surfaced: SurfacedNo,
			evidence: FinalBillingEvidence{
				Source:    EvidenceSourceUnavailable,
				Authority: EvidenceAuthorityUnavailable,
			},
		},
		{
			name:     "evidence_unavailable_winner",
			bLegID:   "b-no-evidence",
			outcome:  LegOutcomeWinner,
			surfaced: SurfacedYes,
			evidence: FinalBillingEvidence{
				Source:    EvidenceSourceUnavailable,
				Authority: EvidenceAuthorityUnavailable,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src := testCallLegUsageRecord(callID, tc.bLegID)
			src.Outcome = tc.outcome
			src.Surfaced = tc.surfaced
			src.Evidence = tc.evidence
			if tc.outcome == LegOutcomeNeverStarted {
				src.FinishedAt = src.StartedAt
			}
			sealed, err := src.Seal()
			if err != nil {
				t.Fatalf("seal %s: %v", tc.name, err)
			}
			if sealed.Outcome != tc.outcome {
				t.Fatalf("outcome = %q, want %q", sealed.Outcome, tc.outcome)
			}
			if sealed.Evidence.Source != tc.evidence.Source {
				t.Fatalf("evidence source = %q, want %q", sealed.Evidence.Source, tc.evidence.Source)
			}
		})
	}
}

func TestCallLegUsageBLegIDTrimIsConsistentBetweenKeyAndFingerprint(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	padded := testCallLegUsageRecord(callID, "  b-trim  ")
	sealed, err := padded.Seal()
	if err != nil {
		t.Fatalf("seal padded BLegID: %v", err)
	}
	wantKey, err := CallLegUsageKey(callID, "b-trim")
	if err != nil {
		t.Fatal(err)
	}
	if sealed.Key != wantKey {
		t.Fatalf("key = %q, want trimmed %q", sealed.Key, wantKey)
	}
	if sealed.BLegID != "b-trim" {
		t.Fatalf("stored BLegID = %q, want trimmed b-trim", sealed.BLegID)
	}
	trimmed := testCallLegUsageRecord(callID, "b-trim")
	trimmedSealed, err := trimmed.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if sealed.Fingerprint != trimmedSealed.Fingerprint {
		t.Fatal("key and fingerprint must both use trimmed BLegID")
	}
	if err := CheckCallLegUsageReplay(sealed, trimmedSealed); err != nil {
		t.Fatalf("padded vs trimmed replay: %v", err)
	}
}

func TestCallLegUsageFingerprintDistinguishesQuantityAndCostPresence(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	absent := testCallLegUsageRecord(callID, "b-presence")
	absent.Evidence.InputTokens = Quantity{}
	absent.Evidence.OutputTokens = Quantity{}
	absent.Evidence.Cost = MoneyEvidence{}
	absentSealed, err := absent.Seal()
	if err != nil {
		t.Fatalf("seal absent: %v", err)
	}
	explicitZero := testCallLegUsageRecord(callID, "b-presence")
	explicitZero.Evidence.InputTokens = Quantity{Present: true}
	explicitZero.Evidence.OutputTokens = Quantity{Present: true}
	explicitZero.Evidence.Cost = MoneyEvidence{Currency: "USD", Present: true}
	zeroSealed, err := explicitZero.Seal()
	if err != nil {
		t.Fatalf("seal explicit zero: %v", err)
	}
	if absentSealed.Fingerprint == zeroSealed.Fingerprint {
		t.Fatal("explicit zero quantity/cost must not fingerprint equal to absent")
	}
	if err := CheckCallLegUsageReplay(absentSealed, zeroSealed); !errors.Is(err, ErrReplayConflict) {
		t.Fatalf("absent vs explicit-zero replay = %v, want ErrReplayConflict", err)
	}
}

func TestCallUsageSealDoesNotMutateReceiverExpectedBLegs(t *testing.T) {
	t.Parallel()
	src := testCallUsageRecord(mustBillingCallID(t))
	src.ExpectedBLegIDs[0] = "b-orig"
	sealed, err := src.Seal()
	if err != nil {
		t.Fatal(err)
	}
	sealed.ExpectedBLegIDs[0] = "b-mutated"
	if src.ExpectedBLegIDs[0] != "b-orig" {
		t.Fatal("Seal must copy expected B-leg IDs")
	}
}

func TestCallUsageRejectsDuplicateOrColonExpectedBLegs(t *testing.T) {
	t.Parallel()
	dup := testCallUsageRecord(mustBillingCallID(t))
	dup.ExpectedBLegIDs = []string{"b-1", "b-1"}
	if _, err := dup.Seal(); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("duplicate expected B-leg = %v, want ErrInvalidRecord", err)
	}
	colon := testCallUsageRecord(mustBillingCallID(t))
	colon.ExpectedBLegIDs = []string{"b:1"}
	if _, err := colon.Seal(); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("colon expected B-leg = %v, want ErrInvalidRecord", err)
	}
}

func TestUsagePayloadsExcludePromptsSecretsHeadersAndProviderSDKObjects(t *testing.T) {
	t.Parallel()
	samples := []any{
		CallUsageRecord{},
		CallLegUsageRecord{},
		FinalBillingEvidence{},
	}
	for _, sample := range samples {
		assertBillingSafeUsageType(t, reflect.TypeOf(sample), map[string]struct{}{})
	}

	callID := mustBillingCallID(t)
	call, err := testCallUsageRecord(callID).Seal()
	if err != nil {
		t.Fatal(err)
	}
	leg, err := testCallLegUsageRecord(callID, "b-1").Seal()
	if err != nil {
		t.Fatal(err)
	}
	for _, payload := range []any{call, leg} {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(string(raw))
		for _, banned := range []string{
			"prompt", "completion", "secret", "authorization_header",
			"authorizationheader", "api_key", "apikey", "bearer ",
		} {
			if strings.Contains(lower, banned) {
				t.Fatalf("%T JSON must not contain %q: %s", payload, banned, raw)
			}
		}
	}
}

func mustBillingCallID(t *testing.T) BillingCallID {
	t.Helper()
	id, err := NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func assertBillingSafeUsageType(t *testing.T, typ reflect.Type, seen map[string]struct{}) {
	t.Helper()
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		assertBillingSafeUsagePkg(t, typ)
		return
	}
	id := typ.PkgPath() + "." + typ.Name()
	if _, ok := seen[id]; ok {
		return
	}
	seen[id] = struct{}{}
	assertBillingSafeUsagePkg(t, typ)
	for field := range typ.Fields() {
		name := strings.ToLower(field.Name)
		for _, banned := range []string{
			"prompt", "completion", "secret", "authorizationheader",
			"authheader", "apikey", "bearer", "sdk",
		} {
			if strings.Contains(name, banned) {
				t.Fatalf("%s.%s is forbidden on usage payloads (8.6)", typ.Name(), field.Name)
			}
		}
		tag := strings.ToLower(string(field.Tag))
		if strings.Contains(tag, "authorization") && strings.Contains(tag, "header") {
			t.Fatalf("%s.%s tag leaks authorization headers: %s", typ.Name(), field.Name, field.Tag)
		}
		assertBillingSafeUsageType(t, field.Type, seen)
	}
}

func assertBillingSafeUsagePkg(t *testing.T, typ reflect.Type) {
	t.Helper()
	pkg := strings.ToLower(typ.PkgPath())
	if pkg == "" {
		return
	}
	for _, banned := range []string{
		"openai", "anthropic", "bedrock", "genai", "aws-sdk", "net/http", "pkg/aipapi",
	} {
		if strings.Contains(pkg, banned) {
			t.Fatalf("usage payload type %s imports forbidden package %q (8.6)", typ, typ.PkgPath())
		}
	}
}
