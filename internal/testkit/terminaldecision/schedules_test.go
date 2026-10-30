package terminaldecision

import "testing"

func TestBarrierRequiresExplicitArrivalAndRelease(t *testing.T) {
	barrier := NewBarrier()

	select {
	case <-barrier.Arrived():
		t.Fatal("barrier arrived before the schedule advanced it")
	default:
	}
	barrier.Arrive()
	select {
	case <-barrier.Arrived():
	default:
		t.Fatal("barrier did not record arrival")
	}
	select {
	case <-barrier.Released():
		t.Fatal("barrier released before the schedule advanced it")
	default:
	}
	barrier.Release()
	select {
	case <-barrier.Released():
	default:
		t.Fatal("barrier did not record release")
	}

	// Replaying a schedule step is an idempotent no-op, not a channel panic.
	barrier.Arrive()
	barrier.Release()
}

func TestRecorderCapturesOneOutcomePerCategory(t *testing.T) {
	recorder := NewRecorder()
	want := Outcome{
		Terminal:   TerminalAllowStop,
		Settlement: SettlementB1,
		Cleanup:    CleanupNone,
		Policy:     PolicySnapshotUnchanged,
	}
	for _, event := range []struct {
		kind  EventKind
		value string
	}{
		{TerminalEvent, want.Terminal},
		{SettlementEvent, want.Settlement},
		{CleanupEvent, want.Cleanup},
		{PolicyEvent, want.Policy},
	} {
		if err := recorder.Record(event.kind, event.value); err != nil {
			t.Fatalf("record %v: %v", event.kind, err)
		}
	}
	if err := recorder.Matches(want); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Record(TerminalEvent, "duplicate"); err == nil {
		t.Fatal("duplicate terminal outcome was accepted")
	}
}

func TestProviderFailureSchedules(t *testing.T) {
	schedules := ProviderFailureSchedules()
	if len(schedules) != 4 {
		t.Fatalf("provider failure schedule count: want 4, got %d", len(schedules))
	}
	for _, schedule := range schedules {
		if schedule.Provider == nil {
			t.Fatalf("%s has no provider barrier", schedule.Name)
		}
		if schedule.Expected.Terminal != TerminalAllowStop ||
			schedule.Expected.Settlement != SettlementB1 ||
			schedule.Expected.Cleanup != CleanupNone {
			t.Fatalf("%s has unsafe failure outcome: %+v", schedule.Name, schedule.Expected)
		}
	}
}

func TestEveryNamedScheduleExecutesItsExpectedEvents(t *testing.T) {
	provider := ProviderFailureSchedules()
	cancellation := CancellationRaceSchedule()
	continuation := ContinuationSchedules()
	withdrawal := GenerationWithdrawalSchedule()
	policy := PolicySchedules()
	overlay := StaleOverlayCleanupSchedule()
	tests := []struct {
		name    string
		fixture *Fixture
	}{
		{provider[0].Name, &provider[0].Fixture},
		{provider[1].Name, &provider[1].Fixture},
		{provider[2].Name, &provider[2].Fixture},
		{provider[3].Name, &provider[3].Fixture},
		{cancellation.Name, &cancellation.Fixture},
		{continuation[0].Name, &continuation[0].Fixture},
		{continuation[1].Name, &continuation[1].Fixture},
		{withdrawal.Name, &withdrawal.Fixture},
		{policy[0].Name, &policy[0].Fixture},
		{policy[1].Name, &policy[1].Fixture},
		{overlay.Name, &overlay.Fixture},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, event := range tc.fixture.ExpectedEvents {
				if err := tc.fixture.Record(event.Kind, event.Value); err != nil {
					t.Fatalf("record expected event %+v: %v", event, err)
				}
			}
			if err := tc.fixture.Verify(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestVerifyRejectsWrongOrderAndMissingEvents(t *testing.T) {
	schedule := B2PublishedSettlementSchedule()
	if len(schedule.ExpectedEvents) < 3 {
		t.Fatalf("expected an ordered continuation sequence, got %d events", len(schedule.ExpectedEvents))
	}
	wrongOrder := append([]Event(nil), schedule.ExpectedEvents...)
	wrongOrder[1], wrongOrder[2] = wrongOrder[2], wrongOrder[1]
	for _, event := range wrongOrder {
		if err := schedule.Record(event.Kind, event.Value); err != nil {
			t.Fatalf("record wrong-order event %+v: %v", event, err)
		}
	}
	if err := schedule.Verify(); err == nil {
		t.Fatal("Verify accepted B1 settlement before B2 publication")
	}

	missing := B2PublishedSettlementSchedule()
	for _, event := range missing.ExpectedEvents[:len(missing.ExpectedEvents)-1] {
		if err := missing.Record(event.Kind, event.Value); err != nil {
			t.Fatalf("record missing-event prefix %+v: %v", event, err)
		}
	}
	if err := missing.Verify(); err == nil {
		t.Fatal("Verify accepted an incomplete schedule")
	}
}

func TestAdmissionFailureRejectsB1Settlement(t *testing.T) {
	schedule := B2AdmissionFailureSchedule()
	for _, event := range schedule.ExpectedEvents {
		if event.Kind == StepEvent && event.Value == StepB1Settlement {
			t.Fatal("prepublication schedule contains a B1 settlement step")
		}
	}
	wrong := append([]Event(nil), schedule.ExpectedEvents...)
	for i, event := range wrong {
		if event.Kind == SettlementEvent {
			wrong[i].Value = SettlementB1
		}
	}
	for _, event := range wrong {
		if err := schedule.Record(event.Kind, event.Value); err != nil {
			t.Fatalf("record prepublication event %+v: %v", event, err)
		}
	}
	if err := schedule.Verify(); err == nil {
		t.Fatal("Verify accepted B1 settlement before B2 publication")
	}
}

func TestRaceAndLifecycleSchedulesExposeNamedBarriersAndExpectedOutcomes(t *testing.T) {
	cancellation := CancellationRaceSchedule()
	if cancellation.Provider == nil || cancellation.Cancel == nil || cancellation.Continuation == nil {
		t.Fatal("cancellation schedule is missing a race barrier")
	}
	if cancellation.Expected != (Outcome{
		Terminal:   TerminalCancelled,
		Settlement: SettlementB1,
		Cleanup:    CleanupCancelled,
		Policy:     PolicySnapshotUnchanged,
	}) {
		t.Fatalf("cancellation outcome: %+v", cancellation.Expected)
	}

	for _, schedule := range ContinuationSchedules() {
		if schedule.B2Admission == nil || schedule.B2Publication == nil || schedule.B1Settlement == nil {
			t.Fatalf("%s is missing a continuation barrier", schedule.Name)
		}
		if schedule.Expected.Terminal == "" || schedule.Expected.Settlement == "" || schedule.Expected.Cleanup == "" {
			t.Fatalf("%s has incomplete continuation outcome: %+v", schedule.Name, schedule.Expected)
		}
	}

	withdrawal := GenerationWithdrawalSchedule()
	if withdrawal.PinnedRequest == nil || withdrawal.Withdrawal == nil || withdrawal.Drain == nil || withdrawal.Close == nil {
		t.Fatal("withdrawal schedule is missing a lifecycle barrier")
	}
	policy := PolicySchedules()
	if len(policy) != 2 {
		t.Fatalf("policy schedule count: want 2, got %d", len(policy))
	}
	for _, schedule := range policy {
		if schedule.Snapshot == nil || schedule.Write == nil || schedule.SnapshotComplete == nil {
			t.Fatalf("%s is missing a policy barrier", schedule.Name)
		}
		if schedule.Expected.Policy != PolicyOldSnapshot && schedule.Expected.Policy != PolicyNewSnapshot {
			t.Fatalf("%s has mixed/unknown policy outcome: %+v", schedule.Name, schedule.Expected)
		}
	}
	overlay := StaleOverlayCleanupSchedule()
	if overlay.ExternalIngress == nil || overlay.OverlayCleanup == nil {
		t.Fatal("stale overlay schedule is missing a cleanup barrier")
	}
	if overlay.Expected.Cleanup != CleanupOverlayDeactivated {
		t.Fatalf("stale overlay cleanup outcome: %+v", overlay.Expected)
	}
}
