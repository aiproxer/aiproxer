package openresponses_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	frontendlimits "github.com/aiproxer/aiproxer/internal/plugins/frontends/limits"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
	proto "github.com/aiproxer/aiproxer/internal/plugins/protocols/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type memSource struct {
	data []byte
}

func newMemSource(data []byte) *memSource {
	return &memSource{data: data}
}

func (s *memSource) Size() int64 {
	return int64(len(s.data))
}

func (s *memSource) Open() (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s.data)), nil
}

func (s *memSource) Close() error {
	return nil
}

func defaultOpenResponsesProofInput(body []byte, routeSelector string, headers http.Header) frontendpipe.ProofInput {
	return frontendpipe.ProofInput{
		Ctx:                  context.Background(),
		Headers:              headers,
		URLPath:              "/openresponses/v1/responses",
		RouteSelector:        routeSelector,
		RoutePrefixes:        routeselect.NewPrefixSet([]string{"openai", "stub", "custom"}),
		DefaultRouteSelector: "stub:default",
		RouteFromBodyModel:   true,
		Source:               newMemSource(body),
		BodyBytes:            int64(len(body)),
	}
}

// Requirement 13, 17.6: Confirm at implementation time: no legacy full-body resolver; RouteFromBodyModel=true.
func TestOpenResponsesProfile_PrerequisitesAndIdentity(t *testing.T) {
	t.Parallel()

	prof := openresponses.NewProfile()
	if prof.ProfileID() != openresponses.ProfileID {
		t.Fatalf("ProfileID: got %q, want %q", prof.ProfileID(), openresponses.ProfileID)
	}
	if prof.ProfileID() != "openresponses_v1" {
		t.Fatalf("ProfileID constant: got %q, want openresponses_v1", prof.ProfileID())
	}

	// Verify Handler builds pipe with no custom route hook
	h := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
	})
	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req) // triggers buildPipe via pipeOnce

	spec := h.Spec()
	if spec == nil {
		t.Fatal("Handler.Spec() returned nil")
	}
	if spec.ResolvePreAdmissionRoute != nil || spec.ResolveBodyRoute != nil {
		t.Fatal("Handler must NOT configure custom route hooks (Requirement 13.2, 17.6)")
	}
	if !spec.RouteFromBodyModel {
		t.Fatal("Handler must configure RouteFromBodyModel=true (Requirement 4.8, 17.6)")
	}

	// Injected profile is honored via HandlerConfig (Task 17 seam)
	customProf := openresponses.NewProfile()
	h2 := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Profile:              customProf,
	})
	req2 := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", strings.NewReader(`{}`))
	rec2 := httptest.NewRecorder()
	h2.ServeHTTP(rec2, req2)
	if h2.Spec().Profile != customProf {
		t.Fatal("Handler must honor explicit Profile injection")
	}
	if h2.Spec().Profile.ProfileID() != "openresponses_v1" {
		t.Fatalf("Handler pipe profile ID: got %q, want openresponses_v1", h2.Spec().Profile.ProfileID())
	}
}

// Requirement 4, 14, 16, 17: Compile proof for simple no-store create request.
func TestOpenResponsesProfile_CompileProof_SimpleNoStoreCreate(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"gpt-4o","input":"hello world","store":false}`)
	prof := openresponses.NewProfile()
	in := defaultOpenResponsesProofInput(body, "gpt-4o", nil)

	out, err := prof.CompileProof(context.Background(), in)
	if err != nil {
		t.Fatalf("CompileProof failed: %v", err)
	}

	proof := out.State.Proof
	if proof.ProfileID != "openresponses_v1" {
		t.Fatalf("ProfileID: got %q, want openresponses_v1", proof.ProfileID)
	}
	if proof.Operation != aipapi.OperationOpenResponsesCreate {
		t.Fatalf("Operation: got %q, want %q", proof.Operation, aipapi.OperationOpenResponsesCreate)
	}
	if proof.Delivery != aipapi.DeliveryModeNonStreaming {
		t.Fatalf("Delivery: got %v, want non-streaming", proof.Delivery)
	}
	if proof.ClientModel != "gpt-4o" {
		t.Fatalf("ClientModel: got %q, want gpt-4o", proof.ClientModel)
	}
	if proof.RouteSelector != "gpt-4o" {
		t.Fatalf("RouteSelector: got %q, want gpt-4o", proof.RouteSelector)
	}
	if proof.Mode != largebody.BodyModeIdentityJSON {
		t.Fatalf("BodyMode: got %v, want IdentityJSON", proof.Mode)
	}
	if !proof.Rewrite.NeedsModelRewrite() {
		t.Fatalf("Rewrite.NeedsModelRewrite: got false, want true")
	}

	// Verify exact model span
	if proof.ModelSpan.Length == 0 {
		t.Fatal("ModelSpan length is 0")
	}
	spanBytes := body[proof.ModelSpan.Offset : proof.ModelSpan.Offset+proof.ModelSpan.Length]
	if string(spanBytes) != `"gpt-4o"` {
		t.Fatalf("ModelSpan text: got %q, want %q", string(spanBytes), `"gpt-4o"`)
	}

	// Verify seeds
	seeds := out.State.Seeds
	if seeds.DeterministicToken != proof.Identity.Token() {
		t.Fatal("Seeds token must match proof identity token")
	}
	if seeds.ClientModel != "gpt-4o" {
		t.Fatalf("Seeds ClientModel: got %q, want gpt-4o", seeds.ClientModel)
	}
	if seeds.RouteSelector != "gpt-4o" {
		t.Fatalf("Seeds RouteSelector: got %q, want gpt-4o", seeds.RouteSelector)
	}
	if seeds.Stream {
		t.Fatal("Seeds Stream: got true, want false")
	}
}

// Requirement 4, 14, 16, 17: Compile proof with streaming, item array, options.
func TestOpenResponsesProfile_CompileProof_StreamingAndOptions(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"gpt-4o","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"streaming test"}]}],"stream":true,"store":false,"temperature":0.7,"top_p":0.9,"max_output_tokens":100}`)
	prof := openresponses.NewProfile()
	in := defaultOpenResponsesProofInput(body, "", nil)

	out, err := prof.CompileProof(context.Background(), in)
	if err != nil {
		t.Fatalf("CompileProof failed: %v", err)
	}

	proof := out.State.Proof
	if proof.Delivery != aipapi.DeliveryModeStreaming {
		t.Fatalf("Delivery: got %v, want streaming", proof.Delivery)
	}
	if proof.MaxOutputTokens != 100 {
		t.Fatalf("MaxOutputTokens: got %d, want 100", proof.MaxOutputTokens)
	}
	if !out.State.Seeds.Stream {
		t.Fatal("Seeds Stream: got false, want true")
	}
}

// Requirement 17.4: Explicit store:false gate.
// store:true, missing store, previous_response_id, truncation, background,
// unsupported controls, unknown keys, duplicate keys must decline to canonical.
func TestOpenResponsesProfile_CompileProof_ExplicitStoreFalseGate(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		json string
	}{
		{
			name: "missing store defaults to true",
			json: `{"model":"gpt-4o","input":"hello"}`,
		},
		{
			name: "explicit store true",
			json: `{"model":"gpt-4o","input":"hello","store":true}`,
		},
		{
			name: "store string value",
			json: `{"model":"gpt-4o","input":"hello","store":"false"}`,
		},
		{
			name: "store null value",
			json: `{"model":"gpt-4o","input":"hello","store":null}`,
		},
		{
			name: "previous_response_id present",
			json: `{"model":"gpt-4o","input":"hello","store":false,"previous_response_id":"resp_MDEyMzQ1Njc4OWFiY2RlZg"}`,
		},
		{
			name: "truncation present",
			json: `{"model":"gpt-4o","input":"hello","store":false,"truncation":"auto"}`,
		},
		{
			name: "background true",
			json: `{"model":"gpt-4o","input":"hello","store":false,"background":true}`,
		},
		{
			name: "include present",
			json: `{"model":"gpt-4o","input":"hello","store":false,"include":["message.output_text"]}`,
		},
		{
			name: "presence_penalty present",
			json: `{"model":"gpt-4o","input":"hello","store":false,"presence_penalty":0.5}`,
		},
		{
			name: "frequency_penalty present",
			json: `{"model":"gpt-4o","input":"hello","store":false,"frequency_penalty":0.5}`,
		},
		{
			name: "stream_options present",
			json: `{"model":"gpt-4o","input":"hello","store":false,"stream_options":{"include_usage":true}}`,
		},
		{
			name: "top_logprobs present",
			json: `{"model":"gpt-4o","input":"hello","store":false,"top_logprobs":3}`,
		},
		{
			name: "service_tier present",
			json: `{"model":"gpt-4o","input":"hello","store":false,"service_tier":"default"}`,
		},
		{
			name: "safety_identifier present",
			json: `{"model":"gpt-4o","input":"hello","store":false,"safety_identifier":"safe-1"}`,
		},
		{
			name: "prompt_cache_key present",
			json: `{"model":"gpt-4o","input":"hello","store":false,"prompt_cache_key":"k1"}`,
		},
		{
			name: "prompt_cache_retention present",
			json: `{"model":"gpt-4o","input":"hello","store":false,"prompt_cache_retention":"in_memory"}`,
		},
		{
			name: "max_tool_calls present",
			json: `{"model":"gpt-4o","input":"hello","store":false,"max_tool_calls":2}`,
		},
		{
			name: "unknown top-level key",
			json: `{"model":"gpt-4o","input":"hello","store":false,"custom_field_x":123}`,
		},
		{
			name: "duplicate key model",
			json: `{"model":"gpt-4o","model":"gpt-4o","input":"hello","store":false}`,
		},
		{
			name: "empty model",
			json: `{"model":"","input":"hello","store":false}`,
		},
		{
			name: "empty input",
			json: `{"model":"gpt-4o","input":"","store":false}`,
		},
	}

	prof := openresponses.NewProfile()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := defaultOpenResponsesProofInput([]byte(tc.json), "", nil)
			_, err := prof.CompileProof(context.Background(), in)
			if err == nil {
				t.Fatalf("expected CompileProof to fail and decline to canonical, got nil error for %s", tc.name)
			}
		})
	}
}

// Requirement 17.4: Path verification - only create path allowed.
func TestOpenResponsesProfile_CompileProof_PathVerification(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"gpt-4o","input":"hello","store":false}`)
	prof := openresponses.NewProfile()

	t.Run("compact path rejected", func(t *testing.T) {
		t.Parallel()
		in := defaultOpenResponsesProofInput(body, "", nil)
		in.URLPath = "/openresponses/v1/responses/compact"
		_, err := prof.CompileProof(context.Background(), in)
		if err == nil {
			t.Fatal("expected error for compact path")
		}
	})

	t.Run("non-responses path rejected", func(t *testing.T) {
		t.Parallel()
		in := defaultOpenResponsesProofInput(body, "", nil)
		in.URLPath = "/openresponses/v1/models"
		_, err := prof.CompileProof(context.Background(), in)
		if err == nil {
			t.Fatal("expected error for non-responses path")
		}
	})

	t.Run("short responses path accepted", func(t *testing.T) {
		t.Parallel()
		in := defaultOpenResponsesProofInput(body, "", nil)
		in.URLPath = "/responses"
		_, err := prof.CompileProof(context.Background(), in)
		if err != nil {
			t.Fatalf("unexpected error for /responses path: %v", err)
		}
	})
}

// Requirement 4.8, 17.6: Route selector precedence.
func TestOpenResponsesProfile_CompileProof_RouteSelectorPrecedence(t *testing.T) {
	t.Parallel()

	prof := openresponses.NewProfile()

	t.Run("explicit header selector wins", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"model":"openai:gpt-4o","input":"hello","store":false}`)
		in := defaultOpenResponsesProofInput(body, "custom:override", nil)
		out, err := prof.CompileProof(context.Background(), in)
		if err != nil {
			t.Fatalf("CompileProof failed: %v", err)
		}
		if out.State.Proof.RouteSelector != "custom:override" {
			t.Fatalf("RouteSelector: got %q, want custom:override", out.State.Proof.RouteSelector)
		}
	})

	t.Run("body model used when header selector empty", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"model":"openai:gpt-4o","input":"hello","store":false}`)
		in := defaultOpenResponsesProofInput(body, "", nil)
		out, err := prof.CompileProof(context.Background(), in)
		if err != nil {
			t.Fatalf("CompileProof failed: %v", err)
		}
		if out.State.Proof.RouteSelector != "openai:gpt-4o" {
			t.Fatalf("RouteSelector: got %q, want openai:gpt-4o", out.State.Proof.RouteSelector)
		}
	})

	t.Run("default route selector applied for unprefixed model when prefixes configured", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"model":"gpt-4o","input":"hello","store":false}`)
		in := defaultOpenResponsesProofInput(body, "", nil)
		out, err := prof.CompileProof(context.Background(), in)
		if err != nil {
			t.Fatalf("CompileProof failed: %v", err)
		}
		if out.State.Proof.RouteSelector != "stub:default" {
			t.Fatalf("RouteSelector: got %q, want stub:default", out.State.Proof.RouteSelector)
		}
	})
}

// Requirement 14.2, 16.2: Session input, body metadata rejection, client session hint.
func TestOpenResponsesProfile_CompileProof_SessionInputAndHeaders(t *testing.T) {
	t.Parallel()

	prof := openresponses.NewProfile()

	t.Run("authoritative session headers mapped", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"model":"gpt-4o","input":"hello","store":false}`)
		hdr := make(http.Header)
		hdr.Set("X-AIP-Session-ID", "sess_authoritative_123")
		hdr.Set("X-AIP-A-Leg-ID", "aleg_456")

		in := defaultOpenResponsesProofInput(body, "", hdr)
		out, err := prof.CompileProof(context.Background(), in)
		if err != nil {
			t.Fatalf("CompileProof failed: %v", err)
		}
		if out.State.Proof.Session.AuthoritativeSessionID != "sess_authoritative_123" {
			t.Fatalf("AuthoritativeSessionID: got %q, want sess_authoritative_123", out.State.Proof.Session.AuthoritativeSessionID)
		}
		if out.State.Proof.Session.ALegID != "aleg_456" {
			t.Fatalf("ALegID: got %q, want aleg_456", out.State.Proof.Session.ALegID)
		}
	})

	t.Run("body session metadata rejected", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"model":"gpt-4o","input":"hello","store":false,"metadata":{"aip_session_id":"sess_spoofed"}}`)
		in := defaultOpenResponsesProofInput(body, "", nil)
		_, err := prof.CompileProof(context.Background(), in)
		if err == nil {
			t.Fatal("expected error for body session metadata")
		}
		if !errors.Is(err, largebody.ErrBodySessionMetadataRejected) {
			t.Fatalf("expected ErrBodySessionMetadataRejected, got %v", err)
		}
	})

	t.Run("client session ID hint declines to canonical", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"model":"gpt-4o","input":"hello","store":false}`)
		hdr := make(http.Header)
		hdr.Set("X-AIP-Session-Hint", "hint_789")

		in := defaultOpenResponsesProofInput(body, "", hdr)
		_, err := prof.CompileProof(context.Background(), in)
		if err == nil {
			t.Fatal("expected error for client session hint")
		}
		if !strings.Contains(err.Error(), "session hint requires canonical decode") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})
}

// Requirement 16: Exact canonical semantic identity parity with CanonicalCallIdentity.
func TestOpenResponsesProfile_CompileProof_IdentityParity(t *testing.T) {
	t.Parallel()

	prof := openresponses.NewProfile()

	cases := []struct {
		name string
		json string
	}{
		{
			name: "string input",
			json: `{"model":"gpt-4o","input":"hello world","store":false}`,
		},
		{
			name: "item array input",
			json: `{"model":"gpt-4o","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello item"}]}],"store":false}`,
		},
		{
			name: "with temperature and top_p",
			json: `{"model":"gpt-4o","input":"options test","store":false,"temperature":0.5,"top_p":0.8}`,
		},
		{
			name: "with instructions",
			json: `{"model":"gpt-4o","input":"test","instructions":"be helpful","store":false}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := []byte(tc.json)
			in := defaultOpenResponsesProofInput(body, "", nil)
			out, err := prof.CompileProof(context.Background(), in)
			if err != nil {
				t.Fatalf("CompileProof failed: %v", err)
			}

			// Also decode via canonical proto.DecodeRequest to compare identity
			// Note: stream control is stripped for canonical create if present
			decodeBody := body
			withoutStream, _, err := proto.DecodeRequest(decodeBody, proto.DefaultLimits())
			_ = withoutStream
			if err != nil {
				t.Fatalf("canonical DecodeRequest failed: %v", err)
			}

			// Wire canonicalCall selector to match
			_, call, err := proto.DecodeRequest(decodeBody, proto.DefaultLimits())
			if err != nil {
				t.Fatalf("canonical DecodeRequest: %v", err)
			}
			call.Route = aipapi.RouteIntent{Selector: out.State.Proof.RouteSelector}
			call.Invocation = aipapi.Invocation{
				Operation:    aipapi.OperationOpenResponsesCreate,
				DeliveryMode: out.State.Proof.Delivery,
			}
			canonicalDigest := largebody.CanonicalCallIdentity(&call)
			if out.State.Proof.Identity.Sum() != canonicalDigest.Sum() {
				t.Fatalf("Identity digest mismatch:\n got:  %x\n want: %x", out.State.Proof.Identity.Sum(), canonicalDigest.Sum())
			}
		})
	}
}

// Requirements 4.8, 16.2, 17.6: Selector differential test proving wire Proof.RouteSelector
// equals canonical decode's selector for prefixed body model with no route header
// (both prefix-configured and empty-prefixes cases), and confirming header-selector wins.
func TestOpenResponsesProfile_SelectorDifferential(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name                 string
		routePrefixes        []string
		defaultRouteSelector string
		headerSelector       string
		bodyJSON             string
		wantSelector         string
	}{
		{
			name:                 "prefixed body model with no route header, prefix-configured",
			routePrefixes:        []string{"stub", "openai"},
			defaultRouteSelector: "stub:default",
			headerSelector:       "",
			bodyJSON:             `{"model":"stub:gpt-4o","input":"hello differential","store":false}`,
			wantSelector:         "stub:gpt-4o",
		},
		{
			name:                 "prefixed body model with no route header, empty-prefixes",
			routePrefixes:        nil,
			defaultRouteSelector: "stub:default",
			headerSelector:       "",
			bodyJSON:             `{"model":"stub:gpt-4o","input":"hello differential","store":false}`,
			wantSelector:         "stub:gpt-4o",
		},
		{
			name:                 "header selector wins over body model (precedence header > body > default)",
			routePrefixes:        []string{"stub", "custom"},
			defaultRouteSelector: "stub:default",
			headerSelector:       "custom:override-model",
			bodyJSON:             `{"model":"stub:gpt-4o","input":"hello differential","store":false}`,
			wantSelector:         "custom:override-model",
		},
		{
			name:                 "non-matching body model falls back to default route",
			routePrefixes:        []string{"stub"},
			defaultRouteSelector: "stub:default",
			headerSelector:       "",
			bodyJSON:             `{"model":"other:gpt-4o","input":"hello differential","store":false}`,
			wantSelector:         "stub:default",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := openresponses.NewHandler(openresponses.HandlerConfig{
				AllowUnauthenticated: true,
				Profile:              openresponses.NewProfile(),
				RoutePrefixes:        tc.routePrefixes,
				DefaultRouteSelector: tc.defaultRouteSelector,
			})

			body := []byte(tc.bodyJSON)
			req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewReader(body))
			if tc.headerSelector != "" {
				req.Header.Set("X-AIP-Route", tc.headerSelector)
			}

			// Trigger buildPipe via Spec()
			spec := h.Spec()
			if spec == nil {
				t.Fatal("Handler.Spec() returned nil")
			}

			// 1. Wire Candidate Proof path (under spec flags)
			proofIn := frontendpipe.ProofInput{
				Ctx:                  context.Background(),
				Headers:              req.Header,
				URLPath:              req.URL.Path,
				RouteSelector:        spec.HTTPHeaders.RouteSelector(req.Header),
				RoutePrefixes:        spec.RoutePrefixes,
				DefaultRouteSelector: spec.DefaultRouteSelector,
				RouteFromBodyModel:   spec.RouteFromBodyModel,
				Source:               newMemSource(body),
				BodyBytes:            int64(len(body)),
			}

			proofOut, err := spec.Profile.CompileProof(context.Background(), proofIn)
			if err != nil {
				t.Fatalf("CompileProof failed: %v", err)
			}
			wireProof := proofOut.State.Proof

			// 2. Canonical Decode path
			dctx := frontendpipe.DecodeContext{
				Ctx:           context.Background(),
				Body:          body,
				RouteSelector: spec.HTTPHeaders.RouteSelector(req.Header),
				Headers:       req.Header,
				URLPath:       req.URL.Path,
			}
			canonDecoded, err := spec.Decode(dctx)
			if err != nil {
				t.Fatalf("canonical Decode failed: %v", err)
			}

			// Assert wire Proof.RouteSelector equals canonical decode's selector
			if wireProof.RouteSelector != canonDecoded.RouteSelector {
				t.Fatalf("RouteSelector differential mismatch:\n wire:      %q\n canonical: %q",
					wireProof.RouteSelector, canonDecoded.RouteSelector)
			}

			// Assert both equal the expected selector
			if wireProof.RouteSelector != tc.wantSelector {
				t.Fatalf("RouteSelector: got %q, want %q", wireProof.RouteSelector, tc.wantSelector)
			}

			// Assert identity parity between wire Proof and canonical Call (Requirement 16.2)
			canonDigest := largebody.CanonicalCallIdentity(canonDecoded.Call)
			if wireProof.Identity.Sum() != canonDigest.Sum() {
				t.Fatalf("Identity digest differential mismatch (Req 16.2):\n wire:      %x\n canonical: %x",
					wireProof.Identity.Sum(), canonDigest.Sum())
			}
		})
	}
}

func test1MiBOpenResponsesBody(tb testing.TB, target int) []byte {
	tb.Helper()
	const prefix = `{"model":"gpt-4o","input":"`
	const suffix = `","store":false}`
	pad := target - len(prefix) - len(suffix)
	if pad < 0 {
		tb.Fatalf("target %d smaller than envelope", target)
	}
	var b strings.Builder
	b.Grow(target)
	b.WriteString(prefix)
	b.WriteString(strings.Repeat("a", pad))
	b.WriteString(suffix)
	return []byte(b.String())
}

// Task 19.3 / Remediation Phase 2 (Lane 3):
// Proof-time transient allocation (B/op) must be bounded by
// memory_spool_bytes (64 KiB) + max_semantic_fact_bytes (256 KiB) + fixed buffers (128 KiB) = 448 KiB.
func TestOpenResponsesProfile_CompileProof_TransientAllocBounded(t *testing.T) {
	const target = 1 << 20 // 1 MiB
	body := test1MiBOpenResponsesBody(t, target)

	spoolDir := t.TempDir()
	spill, err := largebody.NewSpillBuffer(largebody.SpillConfig{
		SpoolDir:         spoolDir,
		MemorySpoolBytes: 64 << 10,
		CopyBufferSize:   32 << 10,
	})
	if err != nil {
		t.Fatalf("spill buffer init: %v", err)
	}
	if _, err := spill.Write(body); err != nil {
		t.Fatalf("spill write: %v", err)
	}
	src, err := spill.Complete()
	if err != nil {
		t.Fatalf("spill complete: %v", err)
	}
	t.Cleanup(func() { _ = src.Close() })

	prof := openresponses.NewProfile()
	proofIn := frontendpipe.ProofInput{
		Ctx:                  context.Background(),
		Headers:              make(http.Header),
		URLPath:              "/openresponses/v1/responses",
		RouteSelector:        "stub:gpt-4o",
		RoutePrefixes:        routeselect.NewPrefixSet([]string{"stub", "openai"}),
		DefaultRouteSelector: "stub:gpt-4o",
		RouteFromBodyModel:   true,
		Source:               src,
		BodyBytes:            src.Size(),
	}

	var proofSink frontendpipe.ProofOutput
	benchRes := testing.Benchmark(func(b *testing.B) {
		b.Helper()
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			out, perr := prof.CompileProof(b.Context(), proofIn)
			if perr != nil {
				b.Fatalf("CompileProof failed: %v", perr)
			}
			proofSink = out
		}
	})
	_ = proofSink

	allocBytesPerOp := benchRes.AllocedBytesPerOp()
	const maxAllowedProofBytesPerOp = int64(64<<10) + int64(256<<10) + int64(128<<10) // 448 KiB

	t.Logf("[openresponses/1MiB] proof-time transient: %d B/op (ceiling: %d B/op)",
		allocBytesPerOp, maxAllowedProofBytesPerOp)

	if allocBytesPerOp > maxAllowedProofBytesPerOp {
		t.Fatalf("proof-time B/op (%d B) must be bounded by %d B, but exceeded target invariant by %d B",
			allocBytesPerOp, maxAllowedProofBytesPerOp, allocBytesPerOp-maxAllowedProofBytesPerOp)
	}
}

// Item 4: Envelope field (model) exceeding DefaultMaxSemanticFactBytes (256 KiB)
// must be bounded and return ErrSemanticFactBudgetExceeded.
func TestOpenResponsesProfile_CompileProof_EnvelopeFactBudget(t *testing.T) {
	t.Parallel()

	prof := openresponses.NewProfile()
	// Create model larger than 256 KiB budget (e.g. 270 KiB)
	largeModel := strings.Repeat("m", 270*1024)
	body := fmt.Appendf(nil, `{"model":"%s","input":"Hello","store":false}`, largeModel)
	in := defaultOpenResponsesProofInput(body, "stub:gpt-4o", nil)

	_, err := prof.CompileProof(context.Background(), in)
	if err == nil {
		t.Fatal("expected error for model exceeding fact budget, got nil")
	}
	if !errors.Is(err, largebody.ErrSemanticFactBudgetExceeded) {
		t.Fatalf("expected ErrSemanticFactBudgetExceeded, got: %v", err)
	}
}

// Item 1: Padded string differential.
func TestOpenResponsesProfile_CompileProof_PaddedStringDifferential(t *testing.T) {
	t.Parallel()

	prof := openresponses.NewProfile()
	body := []byte(`{"model":"gpt-4o","input":"  \n\t  Hello, padded string differential!  \r\n  ","store":false}`)
	in := defaultOpenResponsesProofInput(body, "stub:gpt-4o", nil)

	out, err := prof.CompileProof(context.Background(), in)
	if err != nil {
		t.Fatalf("CompileProof failed: %v", err)
	}

	canonDecoded, err := openresponses.AuthenticateAndDecodeCreate(context.Background(), body, openresponses.DecodeCreateOptions{
		RouteSelector: "stub:gpt-4o",
	})
	if err != nil {
		t.Fatalf("canonical AuthenticateAndDecodeCreate failed: %v", err)
	}
	wantDigest := largebody.CanonicalCallIdentity(canonDecoded.Call)
	if out.State.Proof.Identity.Sum() != wantDigest.Sum() {
		t.Fatalf("Identity mismatch for padded string:\ngot:  %x\nwant: %x", out.State.Proof.Identity.Sum(), wantDigest.Sum())
	}

	wantTurnShape, err := largebody.ClientTurnShapeFromCall(canonDecoded.Call, frontendpipe.DefaultMaxSemanticFactBytes)
	if err != nil {
		t.Fatalf("canonical ClientTurnShapeFromCall failed: %v", err)
	}
	if out.State.Proof.Turn.TotalContentBytes != wantTurnShape.TotalContentBytes {
		t.Fatalf("Turn.TotalContentBytes mismatch: got %d, want %d", out.State.Proof.Turn.TotalContentBytes, wantTurnShape.TotalContentBytes)
	}
}

// Item 1 & 2: Large array differential with mixed roles (system, user, assistant).
// Payload exceeds 2*DefaultMaxSemanticFactBytes (512 KiB) to trigger the streaming large array path.
func TestOpenResponsesProfile_CompileProof_MixedRoleLargeArrayDifferential(t *testing.T) {
	t.Parallel()

	prof := openresponses.NewProfile()
	chunkU := strings.Repeat("u", 280*1024)
	chunkV := strings.Repeat("v", 280*1024)

	var sb strings.Builder
	sb.WriteString(`{"model":"gpt-4o","store":false,"input":[`)
	sb.WriteString(`{"type":"message","role":"system","content":"System prompt initialization."}`)
	sb.WriteString(`,{"type":"message","role":"user","content":"` + chunkU + `"}`)
	sb.WriteString(`,{"type":"message","role":"assistant","content":"Understood, processing your data."}`)
	sb.WriteString(`,{"type":"message","role":"user","content":"` + chunkV + `"}`)
	sb.WriteString(`]}`)

	body := []byte(sb.String())
	in := defaultOpenResponsesProofInput(body, "stub:gpt-4o", nil)

	out, err := prof.CompileProof(context.Background(), in)
	if err != nil {
		t.Fatalf("CompileProof failed for mixed-role large array: %v", err)
	}

	proof := out.State.Proof

	// Verify roles in ClientTurnShape
	if len(proof.Turn.Items) != 4 {
		t.Fatalf("Proof.Turn.Items count: got %d, want 4", len(proof.Turn.Items))
	}
	expectedRoles := []aipapi.Role{
		aipapi.RoleSystem,
		aipapi.RoleUser,
		aipapi.RoleAssistant,
		aipapi.RoleUser,
	}
	for i, expectedRole := range expectedRoles {
		if proof.Turn.Items[i].Role != expectedRole {
			t.Errorf("Turn.Items[%d].Role: got %v, want %v", i, proof.Turn.Items[i].Role, expectedRole)
		}
	}

	// Compare with canonical decode
	canonDecoded, err := openresponses.AuthenticateAndDecodeCreate(context.Background(), body, openresponses.DecodeCreateOptions{
		RouteSelector: "stub:gpt-4o",
	})
	if err != nil {
		t.Fatalf("canonical AuthenticateAndDecodeCreate failed: %v", err)
	}
	wantDigest := largebody.CanonicalCallIdentity(canonDecoded.Call)
	if proof.Identity.Sum() != wantDigest.Sum() {
		t.Fatalf("Identity mismatch for mixed-role large array:\ngot:  %x\nwant: %x", proof.Identity.Sum(), wantDigest.Sum())
	}
}

// Item 1: Padded large array differential.
// Large array payload (> 512 KiB) where content strings have leading and trailing whitespace.
func TestOpenResponsesProfile_CompileProof_PaddedLargeArrayDifferential(t *testing.T) {
	t.Parallel()

	prof := openresponses.NewProfile()
	// Large array exceeding 512 KiB with padded message contents
	chunkU := "  \\n\\t  " + strings.Repeat("u", 280*1024) + "  \\r\\n  "
	chunkV := " \\t " + strings.Repeat("v", 280*1024) + " \\n "

	var sb strings.Builder
	sb.WriteString(`{"model":"gpt-4o","store":false,"input":[`)
	sb.WriteString(`{"type":"message","role":"system","content":"  \\nSystem prompt initialization.  \\t"}`)
	sb.WriteString(`,{"type":"message","role":"user","content":"` + chunkU + `"}`)
	sb.WriteString(`,{"type":"message","role":"assistant","content":"  Understood, processing your data.  \\r\\n"}`)
	sb.WriteString(`,{"type":"message","role":"user","content":"` + chunkV + `"}`)
	sb.WriteString(`]}`)

	body := []byte(sb.String())
	in := defaultOpenResponsesProofInput(body, "stub:gpt-4o", nil)

	out, err := prof.CompileProof(context.Background(), in)
	if err != nil {
		t.Fatalf("CompileProof failed for padded large array: %v", err)
	}

	proof := out.State.Proof

	canonDecoded, err := openresponses.AuthenticateAndDecodeCreate(context.Background(), body, openresponses.DecodeCreateOptions{
		RouteSelector: "stub:gpt-4o",
	})
	if err != nil {
		t.Fatalf("canonical AuthenticateAndDecodeCreate failed: %v", err)
	}
	wantDigest := largebody.CanonicalCallIdentity(canonDecoded.Call)
	if proof.Identity.Sum() != wantDigest.Sum() {
		t.Fatalf("Identity mismatch for padded large array:\ngot:  %x\nwant: %x", proof.Identity.Sum(), wantDigest.Sum())
	}

	// Verify that turn content bytes match canonical
	wantTurnShape, err := largebody.ClientTurnShapeFromCall(canonDecoded.Call, frontendpipe.DefaultMaxSemanticFactBytes)
	if err != nil {
		t.Fatalf("canonical ClientTurnShapeFromCall failed: %v", err)
	}
	if proof.Turn.TotalContentBytes != wantTurnShape.TotalContentBytes {
		t.Fatalf("Turn.TotalContentBytes mismatch: got %d, want %d", proof.Turn.TotalContentBytes, wantTurnShape.TotalContentBytes)
	}
	for i := range proof.Turn.Items {
		if proof.Turn.Items[i].Parts[0].ContentBytes != wantTurnShape.Items[i].Parts[0].ContentBytes {
			t.Fatalf("Turn.Items[%d].Parts[0].ContentBytes mismatch: got %d, want %d",
				i, proof.Turn.Items[i].Parts[0].ContentBytes, wantTurnShape.Items[i].Parts[0].ContentBytes)
		}
	}
}

// Item 5: Number tokens spanning the 32 KiB chunk boundary.
func TestOpenResponsesProfile_CompileProof_ChunkBoundaryNumbersDifferential(t *testing.T) {
	t.Parallel()

	prof := openresponses.NewProfile()

	cases := []struct {
		name       string
		optionKey  string
		numLiteral string
		verify     func(t *testing.T, proof largebody.Proof, canon *aipapi.Call)
	}{
		{
			name:       "max_output_tokens straddling 32768",
			optionKey:  "max_output_tokens",
			numLiteral: "1500",
			verify: func(t *testing.T, proof largebody.Proof, canon *aipapi.Call) {
				t.Helper()
				if proof.MaxOutputTokens != 1500 {
					t.Fatalf("MaxOutputTokens dropped: got %d, want 1500", proof.MaxOutputTokens)
				}
			},
		},
		{
			name:       "temperature straddling 32768",
			optionKey:  "temperature",
			numLiteral: "0.7",
		},
		{
			name:       "top_p straddling 32768",
			optionKey:  "top_p",
			numLiteral: "0.85",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Target offset for number start: 32766 (straddling 32768 chunk boundary)
			targetOffset := 32766
			prefix1 := `{"model":"gpt-4o","store":false,"metadata":{"pad":"`
			suffix1 := `"},"` + tc.optionKey + `":`
			padLen := targetOffset - len(prefix1) - len(suffix1)
			if padLen < 0 {
				t.Fatalf("padLen %d < 0", padLen)
			}
			pad := strings.Repeat("x", padLen)
			tail := `,"input":"hello"}`

			body := []byte(prefix1 + pad + suffix1 + tc.numLiteral + tail)

			// Verify that the number token actually straddles offset 32768:
			numStart := len(prefix1) + padLen + len(suffix1)
			numEnd := numStart + len(tc.numLiteral)
			if numStart >= 32768 || numEnd <= 32768 {
				t.Fatalf("test setup error: number span [%d, %d] does not straddle 32768", numStart, numEnd)
			}

			in := defaultOpenResponsesProofInput(body, "stub:gpt-4o", nil)
			out, err := prof.CompileProof(context.Background(), in)
			if err != nil {
				t.Fatalf("CompileProof failed: %v", err)
			}

			proof := out.State.Proof

			canonDecoded, err := openresponses.AuthenticateAndDecodeCreate(context.Background(), body, openresponses.DecodeCreateOptions{
				RouteSelector: "stub:gpt-4o",
			})
			if err != nil {
				t.Fatalf("canonical AuthenticateAndDecodeCreate failed: %v", err)
			}

			wantDigest := largebody.CanonicalCallIdentity(canonDecoded.Call)
			if proof.Identity.Sum() != wantDigest.Sum() {
				t.Fatalf("Identity mismatch for %s:\ngot:  %x\nwant: %x", tc.name, proof.Identity.Sum(), wantDigest.Sum())
			}
			if tc.verify != nil {
				tc.verify(t, proof, canonDecoded.Call)
			}
		})
	}
}

// Item 3: Fail-open decline corpus testing all scalar/wrong-typed envelope options and controls.
func TestOpenResponsesProfile_CompileProof_DeclineCorpus(t *testing.T) {
	t.Parallel()

	prof := openresponses.NewProfile()

	cases := []struct {
		name       string
		body       string
		wantErrSub string
	}{
		{
			name:       "stream wrong type string",
			body:       `{"model":"gpt-4o","input":"hi","store":false,"stream":"true"}`,
			wantErrSub: "stream must be a boolean",
		},
		{
			name:       "stream wrong type number",
			body:       `{"model":"gpt-4o","input":"hi","store":false,"stream":1}`,
			wantErrSub: "stream must be a boolean",
		},
		{
			name:       "parallel_tool_calls wrong type string",
			body:       `{"model":"gpt-4o","input":"hi","store":false,"parallel_tool_calls":"false"}`,
			wantErrSub: "parallel_tool_calls must be a boolean",
		},
		{
			name:       "temperature wrong type string",
			body:       `{"model":"gpt-4o","input":"hi","store":false,"temperature":"0.7"}`,
			wantErrSub: "temperature must be a number",
		},
		{
			name:       "top_p wrong type string",
			body:       `{"model":"gpt-4o","input":"hi","store":false,"top_p":"0.9"}`,
			wantErrSub: "top_p must be a number",
		},
		{
			name:       "max_output_tokens wrong type string",
			body:       `{"model":"gpt-4o","input":"hi","store":false,"max_output_tokens":"100"}`,
			wantErrSub: "max_output_tokens must be an integer",
		},
		{
			name:       "max_output_tokens float value",
			body:       `{"model":"gpt-4o","input":"hi","store":false,"max_output_tokens":100.5}`,
			wantErrSub: "max_output_tokens must be an integer",
		},
		{
			name:       "instructions wrong type array",
			body:       `{"model":"gpt-4o","input":"hi","store":false,"instructions":["be helpful"]}`,
			wantErrSub: "instructions must be a string",
		},
		{
			name:       "tools wrong type object",
			body:       `{"model":"gpt-4o","input":"hi","store":false,"tools":{"name":"f"}}`,
			wantErrSub: "tools must be an array",
		},
		{
			name:       "tool_choice wrong type number",
			body:       `{"model":"gpt-4o","input":"hi","store":false,"tool_choice":123}`,
			wantErrSub: "tool_choice must be a string or object",
		},
		{
			name:       "metadata wrong type string",
			body:       `{"model":"gpt-4o","input":"hi","store":false,"metadata":"meta"}`,
			wantErrSub: "metadata must be an object",
		},
		{
			name:       "reasoning wrong type string",
			body:       `{"model":"gpt-4o","input":"hi","store":false,"reasoning":"high"}`,
			wantErrSub: "reasoning must be an object",
		},
		{
			name:       "reasoning unsupported field",
			body:       `{"model":"gpt-4o","input":"hi","store":false,"reasoning":{"effort":"high","unsupported":true}}`,
			wantErrSub: "reasoning field \"unsupported\" is unsupported",
		},
		{
			name:       "reasoning invalid effort",
			body:       `{"model":"gpt-4o","input":"hi","store":false,"reasoning":{"effort":"maximum"}}`,
			wantErrSub: "reasoning.effort \"maximum\" is not supported",
		},
		{
			name:       "reasoning empty effort",
			body:       `{"model":"gpt-4o","input":"hi","store":false,"reasoning":{"effort":""}}`,
			wantErrSub: "reasoning.effort must not be empty",
		},
		{
			name:       "input wrong type number",
			body:       `{"model":"gpt-4o","input":12345,"store":false}`,
			wantErrSub: "input must be a string or array",
		},
		{
			name:       "input array item not object",
			body:       `{"model":"gpt-4o","input":["string item"],"store":false}`,
			wantErrSub: "input array item must be an object",
		},
		{
			name:       "large array with reasoning item declines to canonical",
			body:       fmt.Sprintf(`{"model":"gpt-4o","store":false,"input":[{"type":"reasoning","summary":[]},{"type":"message","role":"user","content":"%s"}]}`, strings.Repeat("x", 550*1024)),
			wantErrSub: "requires canonical decode",
		},
		{
			name:       "large array with function_call item declines to canonical",
			body:       fmt.Sprintf(`{"model":"gpt-4o","store":false,"input":[{"type":"function_call","name":"f","arguments":"{}"},{"type":"message","role":"user","content":"%s"}]}`, strings.Repeat("x", 550*1024)),
			wantErrSub: "requires canonical decode",
		},
		{
			name:       "large array with unsupported role declines to canonical",
			body:       fmt.Sprintf(`{"model":"gpt-4o","store":false,"input":[{"type":"message","role":"custom_role","content":"%s"}]}`, strings.Repeat("x", 550*1024)),
			wantErrSub: "requires canonical decode",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			in := defaultOpenResponsesProofInput([]byte(tc.body), "stub:gpt-4o", nil)
			_, err := prof.CompileProof(context.Background(), in)
			if err == nil {
				t.Fatal("expected CompileProof to fail and decline to canonical, got nil error")
			}
			if tc.wantErrSub != "" && !strings.Contains(err.Error(), tc.wantErrSub) {
				t.Fatalf("expected error containing %q, got %q", tc.wantErrSub, err.Error())
			}
		})
	}
}

func TestOpenResponsesProfile_Limits(t *testing.T) {
	t.Parallel()

	prof := openresponses.NewProfile()

	// MaxMessages exceeded
	items := make([]map[string]string, frontendlimits.MaxMessages+1)
	for i := range items {
		items[i] = map[string]string{"type": "message", "role": "user", "content": "hi"}
	}
	body, _ := json.Marshal(map[string]any{
		"model": "gpt-4o",
		"store": false,
		"input": items,
	})
	in := defaultOpenResponsesProofInput(body, "stub:default", nil)
	_, err := prof.CompileProof(context.Background(), in)
	if err == nil || !strings.Contains(err.Error(), "maximum is") {
		t.Fatalf("expected input messages limit error, got %v", err)
	}
}

func TestOpenResponsesProfile_MF1_ToolChoiceParityAndDeclines(t *testing.T) {
	t.Parallel()

	prof := openresponses.NewProfile()

	// 1. Decline cases matching canonical decode
	declineCases := []struct {
		name       string
		body       string
		wantErrSub string
	}{
		{
			name:       "empty string tool_choice declined",
			body:       `{"model":"gpt-4o","input":"hi","store":false,"tool_choice":""}`,
			wantErrSub: "unknown string tool_choice",
		},
		{
			name:       "padded string tool_choice declined",
			body:       `{"model":"gpt-4o","input":"hi","store":false,"tool_choice":" auto "}`,
			wantErrSub: "unknown string tool_choice",
		},
		{
			name:       "allowed_tools missing tools array declined",
			body:       `{"model":"gpt-4o","input":"hi","store":false,"tool_choice":{"type":"allowed_tools"}}`,
			wantErrSub: "allowed_tools requires a tools array",
		},
		{
			name:       "allowed_tools empty tools array declined",
			body:       `{"model":"gpt-4o","input":"hi","store":false,"tool_choice":{"type":"allowed_tools","tools":[]}}`,
			wantErrSub: "allowed_tools tools must not be empty",
		},
	}

	for _, tc := range declineCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := []byte(tc.body)
			in := defaultOpenResponsesProofInput(body, "stub:gpt-4o", nil)
			_, err := prof.CompileProof(context.Background(), in)
			if err == nil {
				t.Fatalf("expected CompileProof to decline with error containing %q, got nil", tc.wantErrSub)
			}
			if !strings.Contains(err.Error(), tc.wantErrSub) {
				t.Fatalf("expected error containing %q, got %q", tc.wantErrSub, err.Error())
			}
		})
	}

	// 2. Parity cases: allowed_tools with refs and allowed_tools with mode "required"
	parityCases := []struct {
		name string
		body string
	}{
		{
			name: "allowed_tools with tool references",
			body: `{"model":"gpt-4o","input":"hi","store":false,"tools":[{"type":"function","name":"search"}],"tool_choice":{"type":"allowed_tools","mode":"auto","tools":[{"type":"function","name":"search"}]}}`,
		},
		{
			name: "allowed_tools with mode required maps to Any",
			body: `{"model":"gpt-4o","input":"hi","store":false,"tools":[{"type":"function","name":"search"}],"tool_choice":{"type":"allowed_tools","mode":"required","tools":[{"type":"function","name":"search"}]}}`,
		},
	}

	for _, tc := range parityCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := []byte(tc.body)
			in := defaultOpenResponsesProofInput(body, "stub:gpt-4o", nil)
			out, err := prof.CompileProof(context.Background(), in)
			if err != nil {
				t.Fatalf("CompileProof failed: %v", err)
			}

			_, canonCall, err := proto.DecodeRequest(body, proto.DefaultLimits())
			if err != nil {
				t.Fatalf("canonical DecodeRequest failed: %v", err)
			}
			canonCall.Route = aipapi.RouteIntent{Selector: out.State.Proof.RouteSelector}
			canonCall.Invocation = aipapi.Invocation{
				Operation:    aipapi.OperationOpenResponsesCreate,
				DeliveryMode: out.State.Proof.Delivery,
			}
			wantDigest := largebody.CanonicalCallIdentity(&canonCall)
			if out.State.Proof.Identity.Sum() != wantDigest.Sum() {
				t.Fatalf("Identity digest mismatch:\n got:  %x\n want: %x", out.State.Proof.Identity.Sum(), wantDigest.Sum())
			}
		})
	}
}

func TestOpenResponsesProfile_MF2_LargeArrayExplicitIDDeclined(t *testing.T) {
	t.Parallel()

	prof := openresponses.NewProfile()

	largeContent := strings.Repeat("x", 550*1024)
	body := fmt.Sprintf(`{"model":"gpt-4o","store":false,"input":[{"type":"message","id":"msg_explicit_123","role":"user","content":"%s"}]}`, largeContent)

	in := defaultOpenResponsesProofInput([]byte(body), "stub:gpt-4o", nil)
	_, err := prof.CompileProof(context.Background(), in)
	if err == nil {
		t.Fatal("expected CompileProof to decline large array with explicit id, got nil error")
	}
	if !strings.Contains(err.Error(), "requires canonical decode") {
		t.Fatalf("expected decline error containing 'requires canonical decode', got %q", err.Error())
	}
}

func TestOpenResponsesProfile_MF3_SmallArrayValidationDeclines(t *testing.T) {
	t.Parallel()

	prof := openresponses.NewProfile()

	cases := []struct {
		name       string
		body       string
		wantErrSub string
	}{
		{
			name:       "orphan function_call_output rejected",
			body:       `{"model":"gpt-4o","store":false,"input":[{"type":"function_call_output","call_id":"orphan_call_999","output":"done"}]}`,
			wantErrSub: "orphan tool result",
		},
		{
			name:       "duplicate item ID rejected",
			body:       `{"model":"gpt-4o","store":false,"input":[{"type":"message","id":"dup_id_1","role":"user","content":"hello"},{"type":"message","id":"dup_id_1","role":"user","content":"world"}]}`,
			wantErrSub: "duplicate item ID",
		},
		{
			name:       "empty item_reference ID rejected",
			body:       `{"model":"gpt-4o","store":false,"input":[{"type":"item_reference","id":""}]}`,
			wantErrSub: "requires canonical decode",
		},
		{
			name: "continuation ref count exceeded rejected",
			body: func() string {
				var items []string
				for i := range 101 {
					items = append(items, fmt.Sprintf(`{"type":"item_reference","id":"ref_%d"}`, i))
				}
				items = append(items, `{"type":"message","role":"user","content":"hi"}`)
				return fmt.Sprintf(`{"model":"gpt-4o","store":false,"input":[%s]}`, strings.Join(items, ","))
			}(),
			wantErrSub: "requires canonical decode",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := []byte(tc.body)
			in := defaultOpenResponsesProofInput(body, "stub:gpt-4o", nil)
			_, err := prof.CompileProof(context.Background(), in)
			if err == nil {
				t.Fatalf("expected CompileProof to fail with %q, got nil error", tc.wantErrSub)
			}
			if !strings.Contains(err.Error(), tc.wantErrSub) {
				t.Fatalf("expected error containing %q, got %q", tc.wantErrSub, err.Error())
			}
		})
	}
}

func TestOpenResponsesProfile_WhitelistDeclines_SmallArray(t *testing.T) {
	t.Parallel()

	prof := openresponses.NewProfile()

	cases := []struct {
		name       string
		body       string
		wantErrSub string
	}{
		{
			name:       "item_reference declined",
			body:       `{"model":"gpt-4o","store":false,"input":[{"type":"item_reference","id":"ref_1"}]}`,
			wantErrSub: "unsupported item type \"item_reference\" requires canonical decode",
		},
		{
			name:       "compaction declined",
			body:       `{"model":"gpt-4o","store":false,"input":[{"type":"compaction","id":"c_1","dialect":"test","encrypted_content":"enc"}]}`,
			wantErrSub: "unsupported item type \"compaction\" requires canonical decode",
		},
		{
			name:       "phase declined",
			body:       `{"model":"gpt-4o","store":false,"input":[{"type":"message","role":"assistant","phase":"commentary","content":"thought"}]}`,
			wantErrSub: "unsupported field \"phase\" requires canonical decode",
		},
		{
			name:       "reasoning declined",
			body:       `{"model":"gpt-4o","store":false,"input":[{"type":"reasoning","content":"thought"}]}`,
			wantErrSub: "unsupported item type \"reasoning\" requires canonical decode",
		},
		{
			name:       "content part annotations declined",
			body:       `{"model":"gpt-4o","store":false,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi","annotations":[{"type":"file_citation"}]}]}]}`,
			wantErrSub: "unsupported content part field \"annotations\" requires canonical decode",
		},
		{
			name:       "content part assistant_ref declined",
			body:       `{"model":"gpt-4o","store":false,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi","assistant_ref":"ref_1"}]}]}`,
			wantErrSub: "unsupported content part field \"assistant_ref\" requires canonical decode",
		},
		{
			name:       "non-text content part type declined",
			body:       `{"model":"gpt-4o","store":false,"input":[{"type":"message","role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/img.png"}}]}]}`,
			wantErrSub: "unsupported content part type \"image_url\" requires canonical decode",
		},
		{
			name:       "unknown message field declined",
			body:       `{"model":"gpt-4o","store":false,"input":[{"type":"message","role":"user","content":"hi","unknown_key":"val"}]}`,
			wantErrSub: "unsupported field \"unknown_key\" requires canonical decode",
		},
		{
			name:       "unknown tool result field declined",
			body:       `{"model":"gpt-4o","store":false,"input":[{"type":"message","role":"user","content":"call"},{"type":"function_call","id":"fc_1","call_id":"c1","name":"fn","arguments":"{}"},{"type":"function_call_output","id":"fco_1","call_id":"c1","output":"ok","unknown_field":"val"}]}`,
			wantErrSub: "unsupported tool result field \"unknown_field\" requires canonical decode",
		},
		{
			name:       "non-text tool output declined",
			body:       `{"model":"gpt-4o","store":false,"input":[{"type":"message","role":"user","content":"call"},{"type":"function_call","id":"fc_1","call_id":"c1","name":"fn","arguments":"{}"},{"type":"function_call_output","id":"fco_1","call_id":"c1","output":[{"type":"image","data":"abc"}]}]}`,
			wantErrSub: "unsupported output part type \"image\" requires canonical decode",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := defaultOpenResponsesProofInput([]byte(tc.body), "gpt-4o", nil)
			_, err := prof.CompileProof(context.Background(), in)
			if err == nil {
				t.Fatalf("expected CompileProof to fail with %q, got nil", tc.wantErrSub)
			}
			if !strings.Contains(err.Error(), tc.wantErrSub) {
				t.Fatalf("expected error containing %q, got %q", tc.wantErrSub, err.Error())
			}
		})
	}
}

func TestOpenResponsesProfile_WhitelistDeclines_LargeArrayParity(t *testing.T) {
	t.Parallel()

	prof := openresponses.NewProfile()
	largeContent := strings.Repeat("x", 200*1024)

	cases := []struct {
		name       string
		body       string
		wantErrSub string
	}{
		{
			name:       "large array item_reference declined",
			body:       fmt.Sprintf(`{"model":"gpt-4o","store":false,"input":[{"type":"item_reference","id":"ref_1"},{"type":"message","role":"user","content":"%s"}]}`, largeContent),
			wantErrSub: "requires canonical decode",
		},
		{
			name:       "large array compaction declined",
			body:       fmt.Sprintf(`{"model":"gpt-4o","store":false,"input":[{"type":"compaction","id":"c_1","dialect":"test","encrypted_content":"enc"},{"type":"message","role":"user","content":"%s"}]}`, largeContent),
			wantErrSub: "requires canonical decode",
		},
		{
			name:       "large array phase declined",
			body:       fmt.Sprintf(`{"model":"gpt-4o","store":false,"input":[{"type":"message","role":"assistant","phase":"commentary","content":"%s"}]}`, largeContent),
			wantErrSub: "requires canonical decode",
		},
		{
			name:       "large array reasoning declined",
			body:       fmt.Sprintf(`{"model":"gpt-4o","store":false,"input":[{"type":"reasoning","content":"thought"},{"type":"message","role":"user","content":"%s"}]}`, largeContent),
			wantErrSub: "requires canonical decode",
		},
		{
			name:       "large array content part annotations declined",
			body:       fmt.Sprintf(`{"model":"gpt-4o","store":false,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"%s","annotations":[{"type":"file_citation"}]}]}]}`, largeContent),
			wantErrSub: "requires canonical decode",
		},
		{
			name:       "large array content part assistant_ref declined",
			body:       fmt.Sprintf(`{"model":"gpt-4o","store":false,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"%s","assistant_ref":"ref_1"}]}]}`, largeContent),
			wantErrSub: "requires canonical decode",
		},
		{
			name:       "large array unknown item field declined",
			body:       fmt.Sprintf(`{"model":"gpt-4o","store":false,"input":[{"type":"message","role":"user","content":"%s","unknown_field":"val"}]}`, largeContent),
			wantErrSub: "requires canonical decode",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := defaultOpenResponsesProofInput([]byte(tc.body), "gpt-4o", nil)
			_, err := prof.CompileProof(context.Background(), in)
			if err == nil {
				t.Fatalf("expected CompileProof to fail for large array parity, got nil error")
			}
			if !strings.Contains(err.Error(), tc.wantErrSub) {
				t.Fatalf("expected error containing %q, got %q", tc.wantErrSub, err.Error())
			}
		})
	}
}

func TestOpenResponsesProfile_WhitelistDeclines_FieldOrderPermutations(t *testing.T) {
	t.Parallel()

	prof := openresponses.NewProfile()

	t.Run("phase before role declines", func(t *testing.T) {
		t.Parallel()
		body := `{"model":"gpt-4o","store":false,"input":[{"phase":"commentary","type":"message","role":"assistant","content":"hello"}]}`
		in := defaultOpenResponsesProofInput([]byte(body), "gpt-4o", nil)
		_, err := prof.CompileProof(context.Background(), in)
		if err == nil || !strings.Contains(err.Error(), "phase") {
			t.Fatalf("expected error mentioning phase, got %v", err)
		}
	})

	t.Run("content before type and role accepted", func(t *testing.T) {
		t.Parallel()
		body := `{"model":"gpt-4o","store":false,"input":[{"content":"hello","type":"message","role":"user"}]}`
		in := defaultOpenResponsesProofInput([]byte(body), "gpt-4o", nil)
		out, err := prof.CompileProof(context.Background(), in)
		if err != nil {
			t.Fatalf("expected valid message with reversed field order to succeed, got %v", err)
		}
		if out.State.Proof.Turn.TotalContentBytes != int64(len("hello")) {
			t.Fatalf("expected content length %d, got %d", len("hello"), out.State.Proof.Turn.TotalContentBytes)
		}
	})

	t.Run("annotations before text in part declines", func(t *testing.T) {
		t.Parallel()
		body := `{"model":"gpt-4o","store":false,"input":[{"type":"message","role":"user","content":[{"annotations":[{"type":"file_citation"}],"type":"input_text","text":"hello"}]}]}`
		in := defaultOpenResponsesProofInput([]byte(body), "gpt-4o", nil)
		_, err := prof.CompileProof(context.Background(), in)
		if err == nil || !strings.Contains(err.Error(), "annotations") {
			t.Fatalf("expected error mentioning annotations, got %v", err)
		}
	})

	t.Run("unknown key before type declines", func(t *testing.T) {
		t.Parallel()
		body := `{"model":"gpt-4o","store":false,"input":[{"custom_field":"val","type":"message","role":"user","content":"hello"}]}`
		in := defaultOpenResponsesProofInput([]byte(body), "gpt-4o", nil)
		_, err := prof.CompileProof(context.Background(), in)
		if err == nil || !strings.Contains(err.Error(), "custom_field") {
			t.Fatalf("expected error mentioning custom_field, got %v", err)
		}
	})
}

func TestOpenResponsesProfile_Tools_RequiredCapabilities(t *testing.T) {
	t.Parallel()

	prof := openresponses.NewProfile()

	hasCap := func(caps []aipapi.Capability, target aipapi.Capability) bool {
		return slices.Contains(caps, target)
	}

	t.Run("tools in envelope", func(t *testing.T) {
		t.Parallel()
		body := `{"model":"gpt-4o","stream":true,"store":false,"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"input":"hello"}`
		in := defaultOpenResponsesProofInput([]byte(body), "gpt-4o", nil)
		out, err := prof.CompileProof(context.Background(), in)
		if err != nil {
			t.Fatalf("CompileProof failed: %v", err)
		}
		caps := out.State.Proof.RequiredCapabilities
		if !hasCap(caps, aipapi.CapabilityStreaming) {
			t.Errorf("expected CapabilityStreaming in %v", caps)
		}
		if !hasCap(caps, aipapi.CapabilityTools) {
			t.Errorf("expected CapabilityTools in %v", caps)
		}
		if !hasCap(caps, aipapi.CapabilityOrderedItems) {
			t.Errorf("expected CapabilityOrderedItems in %v", caps)
		}
	})

	t.Run("tool call and output in input array", func(t *testing.T) {
		t.Parallel()
		body := `{"model":"gpt-4o","stream":true,"store":false,"input":[{"type":"message","role":"user","content":"call tool"},{"type":"function_call","id":"fc_1","call_id":"c1","name":"lookup","arguments":"{}"},{"type":"function_call_output","id":"fco_1","call_id":"c1","output":"result text"}]}`
		in := defaultOpenResponsesProofInput([]byte(body), "gpt-4o", nil)
		out, err := prof.CompileProof(context.Background(), in)
		if err != nil {
			t.Fatalf("CompileProof failed: %v", err)
		}
		caps := out.State.Proof.RequiredCapabilities
		if !hasCap(caps, aipapi.CapabilityStreaming) {
			t.Errorf("expected CapabilityStreaming in %v", caps)
		}
		if !hasCap(caps, aipapi.CapabilityTools) {
			t.Errorf("expected CapabilityTools in %v", caps)
		}
		if !hasCap(caps, aipapi.CapabilityOrderedItems) {
			t.Errorf("expected CapabilityOrderedItems in %v", caps)
		}
	})
}

// Requirement 1.1, 1.2 (large-payload-proof-and-parity): nested role/type/
// status and string-valued store control metadata share the single before-write
// envelope budget. Oversize values decline with the semantic-budget failure;
// whitespace padding counts decoded bytes without discount; cumulative items
// after buffer resets keep charging; exact limit still accepts.
func TestOpenResponsesProfile_NestedMetadataBudget(t *testing.T) {
	t.Parallel()

	prof := openresponses.NewProfile()
	compileWithBudget := func(body []byte, budget int64) (frontendpipe.ProofOutput, error) {
		in := defaultOpenResponsesProofInput(body, "stub:gpt-4o", nil)
		in.Ctx = largebody.WithSemanticFactBudget(context.Background(), budget)
		return prof.CompileProof(context.Background(), in)
	}

	// Oversized string store control: budget must win over store validation.
	bigStore := strings.Repeat("x", 300)
	storeBody := []byte(`{"model":"gpt-4o","store":"` + bigStore + `","input":"hi"}`)
	if _, err := compileWithBudget(storeBody, 100); !errors.Is(err, largebody.ErrSemanticFactBudgetExceeded) {
		t.Fatalf("oversized store: expected budget error, got %v", err)
	}

	// Oversized nested status on the large-array path (body > 2*budget, where
	// item status is otherwise unchecked): budget must decline.
	bigStatus := strings.Repeat("s", 250)
	statusBody := []byte(`{"model":"gpt-4o","store":false,"input":[{"type":"message","role":"user","status":"` + bigStatus + `","content":"hi"}]}`)
	if _, err := compileWithBudget(statusBody, 100); !errors.Is(err, largebody.ErrSemanticFactBudgetExceeded) {
		t.Fatalf("oversized status: expected budget error, got %v", err)
	}

	// Whitespace-padded valid role: 90 spaces + "user" decodes to 94 bytes
	// against model(6) + type(7), so budget 107 accepts and 106 declines.
	paddedRole := strings.Repeat(" ", 90) + "user"
	paddedBody := []byte(`{"model":"gpt-4o","store":false,"input":[{"type":"message","role":"` + paddedRole + `","content":"hi"}]}`)
	if _, err := compileWithBudget(paddedBody, 106); !errors.Is(err, largebody.ErrSemanticFactBudgetExceeded) {
		t.Fatalf("padded role limit+1: expected budget error, got %v", err)
	}
	if _, err := compileWithBudget(paddedBody, 107); err != nil {
		t.Fatalf("padded role at limit: unexpected error: %v", err)
	}

	// Cumulative small items after buffer resets: 2 items with 64-byte padded
	// roles (128) + types (14) + model (6) = 148 against budget 100.
	var sb strings.Builder
	sb.WriteString(`{"model":"gpt-4o","store":false,"input":[`)
	for i := range 2 {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`{"type":"message","role":"` + strings.Repeat(" ", 60) + `user","content":"hi"}`)
	}
	sb.WriteString(`]}`)
	if _, err := compileWithBudget([]byte(sb.String()), 100); !errors.Is(err, largebody.ErrSemanticFactBudgetExceeded) {
		t.Fatalf("cumulative roles: expected budget error, got %v", err)
	}
}

// Requirement 1.1, 1.2: raw envelope capture charges before retaining.
func TestOpenResponsesProfile_RawEnvelopeBudgetBoundary(t *testing.T) {
	t.Parallel()

	prof := openresponses.NewProfile()
	arr := `[{"type":"function","name":"lookup","description":"` + strings.Repeat("d", 64) + `"}]`
	body := []byte(`{"model":"gpt-4o","store":false,"tools":` + arr + `,"input":"hi"}`)
	// Retained metadata: model "gpt-4o" (6 decoded bytes) + tools raw slice.
	// Boolean store:false takes no string sink and charges nothing; string
	// input streams through identity, not the budget.
	wantTotal := int64(6 + len(arr))
	compileWithBudget := func(budget int64) error {
		in := defaultOpenResponsesProofInput(body, "stub:gpt-4o", nil)
		in.Ctx = largebody.WithSemanticFactBudget(context.Background(), budget)
		_, err := prof.CompileProof(context.Background(), in)
		return err
	}
	if err := compileWithBudget(wantTotal - 1); !errors.Is(err, largebody.ErrSemanticFactBudgetExceeded) {
		t.Fatalf("raw envelope limit+1: expected budget error, got %v", err)
	}
	if err := compileWithBudget(wantTotal); err != nil {
		t.Fatalf("raw envelope at limit: unexpected error: %v", err)
	}

	// Chunk-spanning raw capture: a tools array crossing the 32 KiB replay
	// chunk boundary keeps charging each chunk-hook slice against the same
	// remaining budget through the shared before-write leaf.
	bigArr := `[{"type":"function","name":"lookup","description":"` + strings.Repeat("d", 40*1024) + `"}]`
	bigBody := []byte(`{"model":"gpt-4o","store":false,"tools":` + bigArr + `,"input":"hi"}`)
	bigWant := int64(6 + len(bigArr))
	compileBigWithBudget := func(budget int64) error {
		in := defaultOpenResponsesProofInput(bigBody, "stub:gpt-4o", nil)
		in.Ctx = largebody.WithSemanticFactBudget(context.Background(), budget)
		_, err := prof.CompileProof(context.Background(), in)
		return err
	}
	if err := compileBigWithBudget(bigWant - 1); !errors.Is(err, largebody.ErrSemanticFactBudgetExceeded) {
		t.Fatalf("chunk-spanning raw envelope limit+1: expected budget error, got %v", err)
	}
	if err := compileBigWithBudget(bigWant); err != nil {
		t.Fatalf("chunk-spanning raw envelope at limit: unexpected error: %v", err)
	}
}
