package auth

import (
	"testing"
	"time"

	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
)

func TestOpaqueRefDigest_matchesRuntimeHashContract(t *testing.T) {
	t.Parallel()
	const raw = "client-session-plaintext-never-leak"
	got := OpaqueRefDigest(raw)
	if got == "" {
		t.Fatal("expected digest")
	}
	if got == raw {
		t.Fatal("digest must not echo raw client session id")
	}
	if len(got) != 16 {
		t.Fatalf("want 16 hex chars, got %d", len(got))
	}
	// Stable
	if OpaqueRefDigest(raw) != got {
		t.Fatal("digest must be stable")
	}
}

func TestBuildSessionStartEvent_unknownWhenSessionOrALegMissing(t *testing.T) {
	t.Parallel()
	pol := SessionAuditPolicy{
		AccessMode:    sdkauth.AccessSingleUser,
		HandlerKind:   sdkauth.HandlerLocalNoop,
		RequiredLevel: sdkauth.LevelNone,
	}
	ev := BuildSessionStartEvent(SessionStartBuildInput{
		Now:                    time.Unix(1000, 0).UTC(),
		TraceID:                "trace-z",
		Policy:                 pol,
		PrincipalID:            "p1",
		AuthoritativeSessionID: "",
		ClientSessionIDRaw:     "hint",
		ALegID:                 "aleg-1",
		IsNew:                  true,
	})
	if ev.Certainty != sdkauth.SessionCertaintyUnknown {
		t.Fatalf("certainty: want unknown got %q", ev.Certainty)
	}
	ev2 := BuildSessionStartEvent(SessionStartBuildInput{
		Now:                    time.Unix(1000, 0).UTC(),
		TraceID:                "trace-z",
		Policy:                 pol,
		PrincipalID:            "p1",
		AuthoritativeSessionID: "sid-1",
		ClientSessionIDRaw:     "hint",
		ALegID:                 "",
		IsNew:                  true,
	})
	if ev2.Certainty != sdkauth.SessionCertaintyUnknown {
		t.Fatalf("certainty: want unknown got %q", ev2.Certainty)
	}
}

func TestBuildSessionStartEvent_partialWhenSyntheticLocalPrincipal(t *testing.T) {
	t.Parallel()
	pol := SessionAuditPolicy{
		AccessMode:    sdkauth.AccessSingleUser,
		HandlerKind:   sdkauth.HandlerLocalNoop,
		RequiredLevel: sdkauth.LevelNone,
	}
	ev := BuildSessionStartEvent(SessionStartBuildInput{
		Now:                     time.Unix(1100, 0).UTC(),
		TraceID:                 "trace-synth",
		Policy:                  pol,
		PrincipalID:             "local-dev",
		AuthoritativeSessionID:  "sid-2",
		ClientSessionIDRaw:      "c1",
		ALegID:                  "aleg-2",
		IsNew:                   true,
		SyntheticLocalPrincipal: true,
	})
	if ev.Certainty != sdkauth.SessionCertaintyPartial {
		t.Fatalf("certainty: want partial got %q", ev.Certainty)
	}
}

func TestBuildSessionStartEvent_knownWhenStableIdentityAndAuthenticated(t *testing.T) {
	t.Parallel()
	pol := SessionAuditPolicy{
		AccessMode:    sdkauth.AccessMultiUser,
		HandlerKind:   sdkauth.HandlerLocalAPIKey,
		RequiredLevel: sdkauth.LevelAPIKey,
	}
	ev := BuildSessionStartEvent(SessionStartBuildInput{
		Now:                     time.Unix(1200, 0).UTC(),
		TraceID:                 "trace-ok",
		Policy:                  pol,
		PrincipalID:             "user-1",
		AuthoritativeSessionID:  "sid-3",
		ClientSessionIDRaw:      "hint-x",
		ALegID:                  "aleg-3",
		IsNew:                   true,
		SyntheticLocalPrincipal: false,
	})
	if ev.Certainty != sdkauth.SessionCertaintyKnown {
		t.Fatalf("certainty: want known got %q", ev.Certainty)
	}
	if ev.ClientSessionRef != OpaqueRefDigest("hint-x") {
		t.Fatalf("client ref: want digest got %q", ev.ClientSessionRef)
	}
}
