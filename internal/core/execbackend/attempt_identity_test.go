package execbackend

import (
	"context"
	"testing"
)

func TestAttemptIdentityCollector_MergesNonEmptyReports(t *testing.T) {
	ctx, collector := WithAttemptIdentityCollector(context.Background())
	ReportAttemptIdentity(ctx, AttemptIdentity{Cache: "cache-a"})
	ReportAttemptIdentity(ctx, AttemptIdentity{Credential: "cred-a"})
	ReportAttemptIdentity(ctx, AttemptIdentity{Cache: "", Credential: ""})

	got := collector.Identity()
	if got.Cache != "cache-a" || got.Credential != "cred-a" {
		t.Fatalf("identity = %#v, want cache-a/cred-a", got)
	}
}

func TestAttemptIdentityCollector_LastNonEmptyFieldWins(t *testing.T) {
	ctx, collector := WithAttemptIdentityCollector(context.Background())
	ReportAttemptIdentity(ctx, AttemptIdentity{Cache: "first", Credential: "cred-first"})
	ReportAttemptIdentity(ctx, AttemptIdentity{Cache: "second"})

	got := collector.Identity()
	if got.Cache != "second" || got.Credential != "cred-first" {
		t.Fatalf("identity = %#v, want second/cred-first", got)
	}
}

func TestReportAttemptIdentity_WithoutCollectorIsNoop(t *testing.T) {
	ReportAttemptIdentity(context.Background(), AttemptIdentity{Cache: "cache-a"})
	// A nil context must not panic either.
	var nilCtx context.Context
	ReportAttemptIdentity(nilCtx, AttemptIdentity{Cache: "cache-a"})
}

func TestWithAttemptIdentityCollector_NilContext(t *testing.T) {
	var nilCtx context.Context
	ctx, collector := WithAttemptIdentityCollector(nilCtx)
	if ctx == nil || collector == nil {
		t.Fatal("expected a usable context and collector")
	}
	ReportAttemptIdentity(ctx, AttemptIdentity{Credential: "cred-a"})
	if got := collector.Identity(); got.Credential != "cred-a" {
		t.Fatalf("credential = %q, want cred-a", got.Credential)
	}
}
