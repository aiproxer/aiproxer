package largebody_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/jsonshape"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// This file pins the canonical identity/proof output of the generic core so that
// making the mechanism protocol-neutral cannot silently change any byte.
//
// All expected digests below are frozen literals captured from the pre-refactor
// implementation. They are never recomputed with the code's own formula: a
// regression that altered the canonical JSON under the hasher turns one of them
// red immediately.

// goldenOpenAIResponsesInputBody is the OpenAI Responses create body whose
// single-string "input" is decoded by the protocol-owned resolver.
const goldenOpenAIResponsesInputBody = `{"model":"gpt-4o","input":"Hello, how are you today?"}`

func digestHexString(t *testing.T, d largebody.IdentityDigest) string {
	t.Helper()
	sum := d.Sum()
	return hex.EncodeToString(sum[:])
}

// newWriter builds a CallIdentityWriter or fails the test.
func newWriter(t *testing.T, cfg largebody.CallIdentityConfig) *largebody.CallIdentityWriter {
	t.Helper()
	w, err := largebody.NewCallIdentityWriter(cfg)
	if err != nil {
		t.Fatalf("NewCallIdentityWriter: %v", err)
	}
	return w
}

func mustDigest(t *testing.T, w *largebody.CallIdentityWriter) largebody.IdentityDigest {
	t.Helper()
	d, err := w.Digest()
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	return d
}

// addUserTextMessage appends one user message with a single text part.
func addUserTextMessage(t *testing.T, w *largebody.CallIdentityWriter, text string) {
	t.Helper()
	if err := w.StartMessages(); err != nil {
		t.Fatalf("StartMessages: %v", err)
	}
	mw, err := w.BeginMessage(aipapi.RoleUser)
	if err != nil {
		t.Fatalf("BeginMessage: %v", err)
	}
	if err := mw.AddPart(aipapi.TextPart(text)); err != nil {
		t.Fatalf("AddPart: %v", err)
	}
	if err := mw.EndMessage(); err != nil {
		t.Fatalf("EndMessage: %v", err)
	}
}

// TestCallIdentityWriter_GoldenDigests_PreservesEmptyVsNullAndOrdering pins the
// canonical identity for the serialization shapes the fast-path equivalence proof
// depends on: nil versus empty Messages/Items/Instructions, nil versus empty
// Extensions, field ordering, and chunk-split multi-byte UTF-8.
func TestCallIdentityWriter_GoldenDigests_PreservesEmptyVsNullAndOrdering(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		want string
		run  func(t *testing.T) largebody.IdentityDigest
	}{
		{
			name: "zero config",
			want: "9ed821f136a816a7cd7e14b238070f1834814450dc45189049c766e9a4052204",
			run: func(t *testing.T) largebody.IdentityDigest {
				t.Helper()
				return mustDigest(t, newWriter(t, largebody.CallIdentityConfig{}))
			},
		},
		{
			name: "explicit empty Messages array",
			want: "c5183c3d728cc12878a2f9693eae18fd84202ad5498a2835332912ffc5393668",
			run: func(t *testing.T) largebody.IdentityDigest {
				t.Helper()
				w := newWriter(t, largebody.CallIdentityConfig{})
				if err := w.StartMessages(); err != nil {
					t.Fatalf("StartMessages: %v", err)
				}
				return mustDigest(t, w)
			},
		},
		{
			name: "explicit empty Items array",
			want: "d0df1f0a474e43d6208a1c998158abb548d2e3843197c59ab92483423e253eba",
			run: func(t *testing.T) largebody.IdentityDigest {
				t.Helper()
				w := newWriter(t, largebody.CallIdentityConfig{})
				if err := w.StartItems(); err != nil {
					t.Fatalf("StartItems: %v", err)
				}
				return mustDigest(t, w)
			},
		},
		{
			name: "empty non-nil Instructions",
			want: "5e464dc21127ff9691d125f20431436137380ffe37cccba6b0ff358639e2191f",
			run: func(t *testing.T) largebody.IdentityDigest {
				t.Helper()
				return mustDigest(t, newWriter(t, largebody.CallIdentityConfig{
					Instructions: []aipapi.Message{},
				}))
			},
		},
		{
			name: "populated Instructions",
			want: "980308e3b2a111aea3cfce9c1b61afa219828c8437c566db1c95501cb1996407",
			run: func(t *testing.T) largebody.IdentityDigest {
				t.Helper()
				return mustDigest(t, newWriter(t, largebody.CallIdentityConfig{
					Instructions: []aipapi.Message{{
						Role:  aipapi.RoleSystem,
						Parts: []aipapi.Part{aipapi.TextPart("You are precise.")},
					}},
				}))
			},
		},
		{
			name: "empty non-nil Extensions map",
			want: "9391fd5f17d41e8f73394fa8479b675b5d48c4deca60e904fc011877bba5a82d",
			run: func(t *testing.T) largebody.IdentityDigest {
				t.Helper()
				return mustDigest(t, newWriter(t, largebody.CallIdentityConfig{
					Extensions: map[string]json.RawMessage{},
				}))
			},
		},
		{
			name: "field ordering across session route instructions tools options extensions",
			want: "43137bbba605bce9fd98616d1315acf11af76de9e139ed6d8ebda915a76499ec",
			run: func(t *testing.T) largebody.IdentityDigest {
				t.Helper()
				w := newWriter(t, largebody.CallIdentityConfig{
					Session: aipapi.SessionRef{
						AuthoritativeSessionID: "auth-1",
						ClientSessionID:        "client-1",
						ALegID:                 "aleg-1",
						ResumeToken:            "secret-tok",
						Metadata:               map[string]string{"k": "v"},
					},
					RouteSelector: "stub:gpt-4o",
					Instructions: []aipapi.Message{{
						Role:  aipapi.RoleSystem,
						Parts: []aipapi.Part{aipapi.TextPart("You are precise.")},
					}},
					Tools:      []aipapi.ToolDef{{Name: "lookup", Description: "look things up"}},
					ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAny, AllowedTools: []string{"lookup"}},
					Options:    aipapi.GenerationOptions{Temperature: new(0.25)},
					Extensions: map[string]json.RawMessage{
						"acme.model": json.RawMessage(`"acme-large"`),
					},
				})
				addUserTextMessage(t, w, "ordered fields")
				return mustDigest(t, w)
			},
		},
		{
			name: "chunk-split multi-byte UTF-8 streaming text part",
			want: "3d3708cf96dc7d721611a58796906f79b5ffaeb81cda7462165410883883821d",
			run: func(t *testing.T) largebody.IdentityDigest {
				t.Helper()
				w := newWriter(t, largebody.CallIdentityConfig{RouteSelector: "stub:gpt-4o"})
				if err := w.StartMessages(); err != nil {
					t.Fatalf("StartMessages: %v", err)
				}
				mw, err := w.BeginMessage(aipapi.RoleUser)
				if err != nil {
					t.Fatalf("BeginMessage: %v", err)
				}
				pw, err := mw.BeginTextPart()
				if err != nil {
					t.Fatalf("BeginTextPart: %v", err)
				}
				for _, chunk := range []string{"héllo \U0001F30D ", "wörld \U0001F9EA", " end"} {
					if _, err := pw.Write([]byte(chunk)); err != nil {
						t.Fatalf("Write: %v", err)
					}
				}
				if err := pw.Close(); err != nil {
					t.Fatalf("pw.Close: %v", err)
				}
				if err := mw.EndMessage(); err != nil {
					t.Fatalf("EndMessage: %v", err)
				}
				return mustDigest(t, w)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := digestHexString(t, tc.run(t))
			if got != tc.want {
				t.Fatalf("canonical identity digest changed:\ngot:  %s\nwant: %s", got, tc.want)
			}
		})
	}
}

// TestCallIdentityWriter_ProtocolNeutralCallerGetsNoInjectedExtension is the new,
// correct behaviour: a caller that supplies no protocol-specific extension gets
// an identity over exactly the canonical fields it supplied. Core injects nothing.
//
// The negative control keeps the assertion honest: an actually injected
// "openai.model" extension produces a different, also-pinned digest, so an
// injection would turn this test red instead of silently passing.
func TestCallIdentityWriter_ProtocolNeutralCallerGetsNoInjectedExtension(t *testing.T) {
	t.Parallel()

	const (
		neutralDigest    = "c52cd6c228f2e54c9358e008b76bec544ac2b22ad6dff2418088c3a6af87b0eb"
		withOpenAIDigest = "53fba3d6c3275333b155d3be70c23b2957a94cf94ab393862863becd524df98a"
	)

	if neutralDigest == withOpenAIDigest {
		t.Fatal("test is vacuous: the two pinned digests must differ")
	}

	neutral := newWriter(t, largebody.CallIdentityConfig{RouteSelector: "stub:gpt-4o"})
	if got := digestHexString(t, mustDigest(t, neutral)); got != neutralDigest {
		t.Fatalf("protocol-neutral caller identity must carry no injected extension:\ngot:  %s\nwant: %s", got, neutralDigest)
	}

	// Negative control: the same call with an explicitly supplied "openai.model"
	// extension hashes differently, which is exactly what an implicit injection
	// would have produced before this behavior changed.
	withOpenAI := newWriter(t, largebody.CallIdentityConfig{
		RouteSelector: "stub:gpt-4o",
		Extensions: map[string]json.RawMessage{
			"openai.model": json.RawMessage(`"gpt-4o"`),
		},
	})
	if got := digestHexString(t, mustDigest(t, withOpenAI)); got != withOpenAIDigest {
		t.Fatalf("caller-supplied extension digest changed:\ngot:  %s\nwant: %s", got, withOpenAIDigest)
	}
}

// TestCallIdentityWriter_LateModelExtensionIsCallerNamed pins the late model
// discovery path. The extension key comes from the caller; there is no default.
// The late-discovered result must be byte-identical to supplying the same
// extension before streaming starts.
func TestCallIdentityWriter_LateModelExtensionIsCallerNamed(t *testing.T) {
	t.Parallel()

	const wantLate = "d4eef6f6726db66c1e5bc533864fa6a75b5af2f2d99691657a1882f5056a64b2"

	late := newWriter(t, largebody.CallIdentityConfig{
		RouteSelector: "stub:gpt-4o-late",
		Extensions: map[string]json.RawMessage{
			"openrouter.flavor": json.RawMessage(`"responses"`),
		},
	})
	addUserTextMessage(t, late, "late")
	if err := late.SetClientModel("gpt-4o-late", "openairesponses.model"); err != nil {
		t.Fatalf("SetClientModel: %v", err)
	}
	if got := digestHexString(t, mustDigest(t, late)); got != wantLate {
		t.Fatalf("late-model identity digest changed:\ngot:  %s\nwant: %s", got, wantLate)
	}

	eager := newWriter(t, largebody.CallIdentityConfig{
		RouteSelector: "stub:gpt-4o-late",
		Extensions: map[string]json.RawMessage{
			"openrouter.flavor":     json.RawMessage(`"responses"`),
			"openairesponses.model": json.RawMessage(`"gpt-4o-late"`),
		},
	})
	addUserTextMessage(t, eager, "late")
	if got, want := digestHexString(t, mustDigest(t, eager)), wantLate; got != want {
		t.Fatalf("late model discovery diverged from eager supply:\ngot:  %s\nwant: %s", got, want)
	}
}

// TestSetClientModel_RequiresExplicitExtensionKey proves core no longer falls
// back to a protocol-specific extension name when the caller names none.
func TestSetClientModel_RequiresExplicitExtensionKey(t *testing.T) {
	t.Parallel()

	w := newWriter(t, largebody.CallIdentityConfig{RouteSelector: "stub:gpt-4o"})
	if err := w.SetClientModel("gpt-4o"); err == nil {
		t.Fatal("SetClientModel without an extension key must fail instead of choosing a default")
	}
	if err := w.SetClientModel("gpt-4o", "acme.model"); err != nil {
		t.Fatalf("SetClientModel with an explicit key: %v", err)
	}
}

// TestCompileStreamingProof_GoldenDigest_ProtocolSuppliedResolver pins the
// compiled proof digest and body hash for a single-string OpenAI Responses body.
// The expected values were captured while the same mapping still lived in generic
// core, so this proves the move to the protocol edge changed no byte.
func TestCompileStreamingProof_GoldenDigest_ProtocolSuppliedResolver(t *testing.T) {
	t.Parallel()

	const (
		wantDigest = "670018573ddaa710f5498eb253ebaa8a0e14312f829eaaaee6c687a8e13253a1"
		wantHash   = "c1bc6a4de0c2692259a6e426b355539496b915eb11521b39b126d026ce47cf4b"
	)

	body := goldenOpenAIResponsesInputBody
	maxBytes := int64(len(body) + 1024)
	cfg := largebody.StreamingProofConfig{
		Reader:               strings.NewReader(body),
		MaxBytes:             maxBytes,
		ChunkSize:            32,
		StringWriterResolver: openairesponses.InputStringResolver,
		CallIdentityConfig: largebody.CallIdentityConfig{
			RouteSelector: "stub:gpt-4o",
			Extensions: map[string]json.RawMessage{
				"openairesponses.model": json.RawMessage(`"gpt-4o"`),
			},
		},
		ScannerLimits: jsonshape.Limits{
			RejectDuplicateNames: true,
			MaxBytes:             maxBytes,
			MaxDepth:             128,
		},
	}
	res, err := largebody.CompileStreamingProof(context.Background(), cfg)
	if err != nil {
		t.Fatalf("CompileStreamingProof: %v", err)
	}
	if got := digestHexString(t, res.Digest); got != wantDigest {
		t.Fatalf("streaming proof digest changed:\ngot:  %s\nwant: %s", got, wantDigest)
	}
	if got := hex.EncodeToString(res.BodyHash[:]); got != wantHash {
		t.Fatalf("streaming proof body hash changed:\ngot:  %s\nwant: %s", got, wantHash)
	}
	if res.BytesRead != int64(len(body)) {
		t.Fatalf("bytes read: got %d, want %d", res.BytesRead, len(body))
	}
}

// TestCompileStreamingProof_WithoutResolverDecodesNoWireField pins that a caller
// that supplies no resolver gets no wire-field interpretation from core: the
// "input" string is no longer silently mapped onto a canonical user message.
func TestCompileStreamingProof_WithoutResolverDecodesNoWireField(t *testing.T) {
	t.Parallel()

	const wantDigest = "c52cd6c228f2e54c9358e008b76bec544ac2b22ad6dff2418088c3a6af87b0eb"

	body := goldenOpenAIResponsesInputBody
	cfg := largebody.StreamingProofConfig{
		Reader:    strings.NewReader(body),
		MaxBytes:  int64(len(body) + 1024),
		ChunkSize: 32,
		CallIdentityConfig: largebody.CallIdentityConfig{
			RouteSelector: "stub:gpt-4o",
		},
	}
	res, err := largebody.CompileStreamingProof(context.Background(), cfg)
	if err != nil {
		t.Fatalf("CompileStreamingProof: %v", err)
	}

	if got := digestHexString(t, res.Digest); got != wantDigest {
		t.Fatalf("core applied wire-field interpretation without a caller resolver:\ngot:  %s\nwant: %s", got, wantDigest)
	}
}
