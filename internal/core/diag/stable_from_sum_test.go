package diag_test

import (
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func assertParityWithFromSum(t *testing.T, call *aipapi.Call) {
	t.Helper()

	sum := diag.StableCallSum(call)

	// Token parity
	wantToken := diag.StableCallToken(call)
	gotToken := diag.StableCallTokenFromSum(sum)
	if gotToken != wantToken {
		t.Fatalf("StableCallTokenFromSum = %q, want %q", gotToken, wantToken)
	}

	// ID parity
	var explicitID string
	if call != nil {
		explicitID = call.ID
	}
	wantID := diag.StableCallID(call)
	gotID := diag.StableCallIDFromSum(explicitID, sum)
	if gotID != wantID {
		t.Fatalf("StableCallIDFromSum(%q) = %q, want %q", explicitID, gotID, wantID)
	}

	// Unix timestamp parity
	wantUnix := diag.StableUnix(call)
	gotUnix := diag.StableUnixFromSum(sum)
	if gotUnix != wantUnix {
		t.Fatalf("StableUnixFromSum = %d, want %d", gotUnix, wantUnix)
	}

	// UTC Time parity
	wantTime := diag.StableTime(call)
	gotTime := diag.StableTimeFromSum(sum)
	if !gotTime.Equal(wantTime) || gotTime.Location() != time.UTC {
		t.Fatalf("StableTimeFromSum = %v (%v), want %v (%v)", gotTime, gotTime.Location(), wantTime, wantTime.Location())
	}
}

func TestStableFromSum_ParityFixtures(t *testing.T) {
	t.Parallel()

	t.Run("nil call", func(t *testing.T) {
		t.Parallel()
		assertParityWithFromSum(t, nil)
	})

	t.Run("empty call", func(t *testing.T) {
		t.Parallel()
		assertParityWithFromSum(t, &aipapi.Call{})
	})

	t.Run("base call", func(t *testing.T) {
		t.Parallel()
		assertParityWithFromSum(t, freezeBaseCall())
	})

	t.Run("call with explicit ID", func(t *testing.T) {
		t.Parallel()
		c := freezeBaseCall()
		c.ID = "call-explicit-1"
		assertParityWithFromSum(t, c)
	})

	t.Run("call with whitespace ID", func(t *testing.T) {
		t.Parallel()
		c := freezeBaseCall()
		c.ID = "  call-whitespace-2  "
		assertParityWithFromSum(t, c)
	})

	t.Run("call with blank ID", func(t *testing.T) {
		t.Parallel()
		c := freezeBaseCall()
		c.ID = "   "
		assertParityWithFromSum(t, c)
	})

	t.Run("huge string fixture", func(t *testing.T) {
		t.Parallel()
		c := freezeBaseCall()
		c.Messages[0].Parts[0] = aipapi.TextPart(strings.Repeat("a", 1<<20))
		assertParityWithFromSum(t, c)
	})

	t.Run("tricky unicode and html escapes", func(t *testing.T) {
		t.Parallel()
		c := freezeBaseCall()
		c.Messages[0].Parts[0] = aipapi.TextPart("<div>&\"'\\</div>\u2028\u2029🧪 café \\u0041 \n\t\r")
		assertParityWithFromSum(t, c)
	})

	t.Run("tools and tool choice", func(t *testing.T) {
		t.Parallel()
		c := freezeBaseCall()
		c.Tools = []aipapi.ToolDef{{Name: "get_weather", Description: "lookup"}}
		c.ToolChoice = aipapi.ToolChoice{Mode: aipapi.ToolChoiceAny}
		assertParityWithFromSum(t, c)
	})

	t.Run("items shape", func(t *testing.T) {
		t.Parallel()
		c := &aipapi.Call{
			Route: aipapi.RouteIntent{Selector: "stub:gpt-4o-mini"},
			Items: []aipapi.Item{{
				Kind:    aipapi.ItemKindMessage,
				ID:      "m1",
				Status:  aipapi.ItemStatusCompleted,
				Role:    aipapi.RoleUser,
				Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "freeze hello"}},
			}},
		}
		assertParityWithFromSum(t, c)
	})

	t.Run("model and route extensions", func(t *testing.T) {
		t.Parallel()
		c := freezeBaseCall()
		c.Route.Selector = "stub:gpt-4o"
		c.Extensions = freezeModelExt("gpt-4o-mini")
		assertParityWithFromSum(t, c)
	})

	t.Run("session fields", func(t *testing.T) {
		t.Parallel()
		c := freezeBaseCall()
		c.Session.AuthoritativeSessionID = "sess-1"
		c.Session.ClientSessionID = "client-1"
		c.Session.ALegID = "a-1"
		c.Session.ResumeToken = "tok-secret"
		c.Session.ContinuityKey = "ck-1"
		assertParityWithFromSum(t, c)
	})

	t.Run("optional generation options", func(t *testing.T) {
		t.Parallel()
		c := freezeBaseCall()
		c.Options = aipapi.GenerationOptions{
			MaxOutputTokens: new(1024),
			Temperature:     new(0.7),
			TopP:            new(0.9),
			ReasoningEffort: "high",
			Verbosity:       aipapi.VerbosityLow,
		}
		c.PreviousResponseID = "resp_prev"
		c.PromptCacheKey = "cache_key_1"
		assertParityWithFromSum(t, c)
	})
}

func TestStableFromSum_DirectSumProperties(t *testing.T) {
	t.Parallel()

	var zeroSum [32]byte
	if got := diag.StableCallTokenFromSum(zeroSum); got != "0000000000000000" {
		t.Fatalf("zero sum token = %q, want 16 zeros", got)
	}

	if got := diag.StableCallIDFromSum("", zeroSum); got != "call_0000000000000000" {
		t.Fatalf("zero sum empty explicit ID = %q, want call_0000000000000000", got)
	}

	if got := diag.StableCallIDFromSum("custom-id", zeroSum); got != "custom-id" {
		t.Fatalf("zero sum explicit ID = %q, want custom-id", got)
	}

	call := freezeBaseCall()
	sum := diag.StableCallSum(call)
	if sum == zeroSum {
		t.Fatal("base call must not have zero sum")
	}

	// ID difference on call struct does not affect StableCallSum
	callWithID := freezeBaseCall()
	callWithID.ID = "explicit-id-123"
	sumWithID := diag.StableCallSum(callWithID)
	if sumWithID != sum {
		t.Fatal("Call.ID must not affect StableCallSum")
	}
}
