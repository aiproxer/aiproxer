package runtime

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
)

type capturingTurnRecorder struct {
	recorded []app.ClientTurnRecordInput
	failWith error
}

func (c *capturingTurnRecorder) RecordClientTurnAfterGate(_ context.Context, in app.ClientTurnRecordInput) error {
	if c.failWith != nil {
		return c.failWith
	}
	c.recorded = append(c.recorded, in)
	return nil
}

func (c *capturingTurnRecorder) RecordPostHookStreamEvent(_ context.Context, _ app.StreamEventRecordInput) error {
	return nil
}

// TestDifferential_CanonicalVsWireRecorderInput verifies that for any canonical Call,
// the wire path (Call -> ClientTurnShape -> BuildClientTurnRecordInputFromShape) produces
// exact role, ordinal, and part kind lines matching canonical buildClientTurnRecordInput
// without prompt text (Requirements 14.3, 14.5).
func TestDifferential_CanonicalVsWireRecorderInput(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	const traceID = "trace-diff-12345"
	const promptSecret = "SECRET_PROMPT_PAYLOAD_ABCXYZ"

	br := app.BeginResult{
		Record: domain.Record{
			SessionID: "sess-abc-789",
			ALegID:    "aleg-xyz-101",
		},
		TurnID: "turn-1",
		EffectivePolicy: domain.PolicyMetadata{
			PolicyVersion: "v1.0",
		},
	}

	testCases := []struct {
		name string
		call *aipapi.Call
	}{
		{
			name: "single user message with text",
			call: &aipapi.Call{
				Messages: []aipapi.Message{
					{
						Role: aipapi.RoleUser,
						Parts: []aipapi.Part{
							{Kind: aipapi.PartText, Text: promptSecret},
						},
					},
				},
			},
		},
		{
			name: "instructions and multi-turn messages",
			call: &aipapi.Call{
				Instructions: []aipapi.Message{
					{
						Role: aipapi.RoleSystem,
						Parts: []aipapi.Part{
							{Kind: aipapi.PartText, Text: "system: " + promptSecret},
						},
					},
					{
						Role: aipapi.RoleDeveloper,
						Parts: []aipapi.Part{
							{Kind: aipapi.PartText, Text: "developer: " + promptSecret},
						},
					},
				},
				Messages: []aipapi.Message{
					{
						Role: aipapi.RoleUser,
						Parts: []aipapi.Part{
							{Kind: aipapi.PartText, Text: "user: " + promptSecret},
						},
					},
					{
						Role: aipapi.RoleAssistant,
						Parts: []aipapi.Part{
							{Kind: aipapi.PartText, Text: "assistant: " + promptSecret},
						},
					},
					{
						Role: aipapi.RoleTool,
						Parts: []aipapi.Part{
							{Kind: aipapi.PartText, Text: "tool: " + promptSecret},
						},
					},
				},
			},
		},
		{
			name: "multi-part message with text, image, file, reasoning, json",
			call: &aipapi.Call{
				Messages: []aipapi.Message{
					{
						Role: aipapi.RoleUser,
						Parts: []aipapi.Part{
							{Kind: aipapi.PartText, Text: promptSecret},
							{Kind: aipapi.PartImageRef, ImageRef: "https://example.com/cat.png"},
							{Kind: aipapi.PartFileRef, FileRef: "file-id-123"},
							{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{Text: "thinking..."}},
							{Kind: aipapi.PartJSON, Content: []byte(`{"key":"value"}`)},
						},
					},
				},
			},
		},
		{
			name: "item authority with mixed message and tool call items",
			call: &aipapi.Call{
				Items: []aipapi.Item{
					{
						Kind: aipapi.ItemKindMessage,
						Role: aipapi.RoleUser,
						Content: []aipapi.ContentPart{
							{Kind: aipapi.ContentPartText, Text: promptSecret},
						},
					},
					{
						Kind: aipapi.ItemKindToolCall,
						ToolCall: &aipapi.ToolCallItem{
							CallID: "call-1",
							Name:   "search_db",
						},
					},
					{
						Kind: aipapi.ItemKindToolResult,
						ToolResult: &aipapi.ToolResultItem{
							CallID: "call-1",
							Name:   "search_db",
							Output: promptSecret,
						},
					},
					{
						Kind: aipapi.ItemKindReasoning,
						Reasoning: &aipapi.ReasoningItem{
							Reasoning: &aipapi.ReasoningPart{Text: "deep thought"},
						},
					},
					{
						Kind: aipapi.ItemKindCompaction,
						Compaction: &aipapi.CompactionItem{
							EncapsulatedID: "enc-1",
						},
					},
				},
			},
		},
		{
			name: "empty call",
			call: &aipapi.Call{},
		},
	}

	const budget = 256 * 1024

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Canonical recorder input
			canonicalInput := buildClientTurnRecordInput(now, traceID, br, tc.call)

			// Wire path: Call -> ClientTurnShape -> BuildClientTurnRecordInputFromShape
			shape, err := largebody.ClientTurnShapeFromCall(tc.call, budget)
			if err != nil {
				t.Fatalf("ClientTurnShapeFromCall failed: %v", err)
			}

			wireInput, err := BuildClientTurnRecordInputFromShape(now, traceID, br, shape, budget)
			if err != nil {
				t.Fatalf("BuildClientTurnRecordInputFromShape failed: %v", err)
			}

			// 1. Validate envelope scalar fields
			if wireInput.Now != canonicalInput.Now {
				t.Errorf("Now = %v, want %v", wireInput.Now, canonicalInput.Now)
			}
			if wireInput.TraceID != canonicalInput.TraceID {
				t.Errorf("TraceID = %q, want %q", wireInput.TraceID, canonicalInput.TraceID)
			}
			if wireInput.SessionID != canonicalInput.SessionID {
				t.Errorf("SessionID = %v, want %v", wireInput.SessionID, canonicalInput.SessionID)
			}
			if wireInput.TurnID != canonicalInput.TurnID {
				t.Errorf("TurnID = %v, want %v", wireInput.TurnID, canonicalInput.TurnID)
			}
			if wireInput.Policy != canonicalInput.Policy {
				t.Errorf("Policy = %+v, want %+v", wireInput.Policy, canonicalInput.Policy)
			}

			// 2. Validate Lines count and contents
			if len(wireInput.Lines) != len(canonicalInput.Lines) {
				t.Fatalf("Lines count mismatch: got %d, want %d", len(wireInput.Lines), len(canonicalInput.Lines))
			}

			for i := range canonicalInput.Lines {
				cLine := canonicalInput.Lines[i]
				wLine := wireInput.Lines[i]

				if wLine.Role != cLine.Role {
					t.Errorf("line %d: Role = %q, want %q", i, wLine.Role, cLine.Role)
				}
				if wLine.Ordinal != cLine.Ordinal {
					t.Errorf("line %d: Ordinal = %d, want %d", i, wLine.Ordinal, cLine.Ordinal)
				}
				if !reflect.DeepEqual(wLine.Parts, cLine.Parts) {
					t.Errorf("line %d: Parts = %v, want %v", i, wLine.Parts, cLine.Parts)
				}
			}

			// 3. No prompt text anywhere in wireInput or shape
			wireStr := fmt.Sprintf("%+v", wireInput)
			if strings.Contains(wireStr, promptSecret) {
				t.Fatalf("wireInput retains prompt text: %s", wireStr)
			}
			shapeStr := fmt.Sprintf("%+v", shape)
			if strings.Contains(shapeStr, promptSecret) {
				t.Fatalf("shape retains prompt text: %s", shapeStr)
			}
		})
	}
}

// TestRecordClientTurnWithShape_PreparedSecureSession verifies that PreparedSecureSession
// can invoke the secure session recorder directly with a ClientTurnShape (Requirements 14.3, 14.5).
func TestRecordClientTurnWithShape_PreparedSecureSession(t *testing.T) {
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{
		ID:          "usr-rec-test",
		DisplayName: "User Rec Test",
	})
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rawMem := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, rawMem, b2)
	recorder := &capturingTurnRecorder{}

	exec := setSecureSessionDenialMapper(TestExecutor())
	exec.Store = b2
	exec.SecureSession = mgr
	exec.SecureSessionRecorder = recorder
	exec.Now = func() time.Time { return time.Unix(1000, 0) }

	prep, err := exec.PrepareSecureSession(ctx, SecureSessionPrepInput{
		TraceID: "trace-rec-1",
		Session: largebody.SessionInput{
			AuthoritativeSessionID: "",
			ClientSessionID:        "client-sess-rec",
			NewSessionRequested:    true,
		},
	})
	if err != nil {
		t.Fatalf("PrepareSecureSession failed: %v", err)
	}

	br, err := prep.ExecuteBeginTurn(ctx)
	if err != nil {
		t.Fatalf("ExecuteBeginTurn failed: %v", err)
	}

	shape := largebody.ClientTurnShape{
		Items: []largebody.ClientTurnItemShape{
			{
				Kind:    aipapi.ItemKindMessage,
				Role:    aipapi.RoleUser,
				Ordinal: 0,
				Parts: []largebody.ClientTurnPartShape{
					{Kind: aipapi.ContentPartText, ContentBytes: 256},
				},
			},
		},
		TotalContentBytes: 256,
	}

	const budget = 64 * 1024
	if err := prep.RecordClientTurnWithShape(ctx, br, shape, budget); err != nil {
		t.Fatalf("RecordClientTurnWithShape failed: %v", err)
	}

	if len(recorder.recorded) != 1 {
		t.Fatalf("expected 1 recorded turn, got %d", len(recorder.recorded))
	}

	rec := recorder.recorded[0]
	if rec.SessionID != br.Record.SessionID {
		t.Errorf("SessionID = %v, want %v", rec.SessionID, br.Record.SessionID)
	}
	if len(rec.Lines) != 1 {
		t.Fatalf("Lines count = %d, want 1", len(rec.Lines))
	}
	if rec.Lines[0].Role != "user" || rec.Lines[0].Ordinal != 0 || len(rec.Lines[0].Parts) != 1 || rec.Lines[0].Parts[0] != "text" {
		t.Errorf("Lines[0] = %+v, want role: user, ordinal: 0, parts: [text]", rec.Lines[0])
	}
}

// TestRecordClientTurnWithShape_BudgetOverflowFallsBack verifies that if ClientTurnShape
// exceeds the semantic fact budget, RecordClientTurnWithShape fails with ErrSemanticFactBudgetExceeded
// so pre-commit canonical fallback occurs (Requirement 14.4).
func TestRecordClientTurnWithShape_BudgetOverflowFallsBack(t *testing.T) {
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{
		ID:          "usr-overflow-test",
		DisplayName: "User Overflow Test",
	})
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rawMem := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, rawMem, b2)
	recorder := &capturingTurnRecorder{}

	exec := setSecureSessionDenialMapper(TestExecutor())
	exec.Store = b2
	exec.SecureSession = mgr
	exec.SecureSessionRecorder = recorder
	exec.Now = func() time.Time { return time.Unix(1000, 0) }

	prep, err := exec.PrepareSecureSession(ctx, SecureSessionPrepInput{
		TraceID: "trace-overflow-1",
		Session: largebody.SessionInput{
			AuthoritativeSessionID: "",
			ClientSessionID:        "client-sess-overflow",
			NewSessionRequested:    true,
		},
	})
	if err != nil {
		t.Fatalf("PrepareSecureSession failed: %v", err)
	}

	br, err := prep.ExecuteBeginTurn(ctx)
	if err != nil {
		t.Fatalf("ExecuteBeginTurn failed: %v", err)
	}

	// 5 items with a tiny budget of 2
	shape := largebody.ClientTurnShape{
		Items: make([]largebody.ClientTurnItemShape, 5),
	}
	for i := range shape.Items {
		shape.Items[i] = largebody.ClientTurnItemShape{
			Kind:    aipapi.ItemKindMessage,
			Role:    aipapi.RoleUser,
			Ordinal: int64(i),
			Parts: []largebody.ClientTurnPartShape{
				{Kind: aipapi.ContentPartText, ContentBytes: 10},
			},
		}
	}

	err = prep.RecordClientTurnWithShape(ctx, br, shape, 2)
	if err == nil {
		t.Fatal("expected error on budget overflow, got nil")
	}

	if !errors.Is(err, largebody.ErrSemanticFactBudgetExceeded) {
		t.Fatalf("expected error wrapping ErrSemanticFactBudgetExceeded, got %v", err)
	}

	// Recorder must NOT have been called on overflow
	if len(recorder.recorded) != 0 {
		t.Fatalf("expected 0 recorded turns on overflow, got %d", len(recorder.recorded))
	}
}

// TestRecordClientTurnWithShape_MandatoryVsOptionalRecordingFailure verifies error propagation
// when recording fails under mandatory vs optional modes.
func TestRecordClientTurnWithShape_MandatoryVsOptionalRecordingFailure(t *testing.T) {
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{
		ID:          "usr-mand-test",
		DisplayName: "User Mandatory Test",
	})
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rawMem := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, rawMem, b2)

	shape := largebody.ClientTurnShape{
		Items: []largebody.ClientTurnItemShape{
			{
				Kind:    aipapi.ItemKindMessage,
				Role:    aipapi.RoleUser,
				Ordinal: 0,
				Parts: []largebody.ClientTurnPartShape{
					{Kind: aipapi.ContentPartText, ContentBytes: 10},
				},
			},
		},
		TotalContentBytes: 10,
	}
	const budget = 64 * 1024

	// 1. Mandatory recording failure must return error
	{
		recorder := &capturingTurnRecorder{failWith: errors.New("disk full")}
		exec := setSecureSessionDenialMapper(TestExecutor())
		exec.Store = b2
		exec.SecureSession = mgr
		exec.SecureSessionRecorder = recorder
		exec.SecureSessionRecordingMandatory = true
		exec.Now = func() time.Time { return time.Unix(1000, 0) }

		prep, err := exec.PrepareSecureSession(ctx, SecureSessionPrepInput{
			TraceID: "trace-mand-1",
			Session: largebody.SessionInput{NewSessionRequested: true},
		})
		if err != nil {
			t.Fatalf("PrepareSecureSession failed: %v", err)
		}
		br, err := prep.ExecuteBeginTurn(ctx)
		if err != nil {
			t.Fatalf("ExecuteBeginTurn failed: %v", err)
		}

		err = prep.RecordClientTurnWithShape(ctx, br, shape, budget)
		if err == nil {
			t.Fatal("expected error on mandatory recorder failure, got nil")
		}
		if !strings.Contains(err.Error(), "disk full") {
			t.Fatalf("expected error containing 'disk full', got %v", err)
		}
	}

	// 2. Optional recording failure must log and return nil (non-fatal)
	{
		recorder := &capturingTurnRecorder{failWith: errors.New("disk full")}
		exec := setSecureSessionDenialMapper(TestExecutor())
		exec.Store = b2
		exec.SecureSession = mgr
		exec.SecureSessionRecorder = recorder
		exec.SecureSessionRecordingMandatory = false
		exec.Now = func() time.Time { return time.Unix(1000, 0) }

		prep, err := exec.PrepareSecureSession(ctx, SecureSessionPrepInput{
			TraceID: "trace-opt-1",
			Session: largebody.SessionInput{NewSessionRequested: true},
		})
		if err != nil {
			t.Fatalf("PrepareSecureSession failed: %v", err)
		}
		br, err := prep.ExecuteBeginTurn(ctx)
		if err != nil {
			t.Fatalf("ExecuteBeginTurn failed: %v", err)
		}

		err = prep.RecordClientTurnWithShape(ctx, br, shape, budget)
		if err != nil {
			t.Fatalf("expected nil error on optional recorder failure, got %v", err)
		}
	}
}

// TestPreparedSecureSession_BuildClientTurnRecordInputMethod verifies the method on PreparedSecureSession.
func TestPreparedSecureSession_BuildClientTurnRecordInputMethod(t *testing.T) {
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{
		ID:          "usr-meth-test",
		DisplayName: "User Method Test",
	})
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rawMem := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, rawMem, b2)

	exec := setSecureSessionDenialMapper(TestExecutor())
	exec.Store = b2
	exec.SecureSession = mgr
	exec.Now = func() time.Time { return time.Unix(1000, 0) }

	prep, err := exec.PrepareSecureSession(ctx, SecureSessionPrepInput{
		TraceID: "trace-meth-1",
		Session: largebody.SessionInput{NewSessionRequested: true},
	})
	if err != nil {
		t.Fatalf("PrepareSecureSession failed: %v", err)
	}
	br, err := prep.ExecuteBeginTurn(ctx)
	if err != nil {
		t.Fatalf("ExecuteBeginTurn failed: %v", err)
	}

	shape := largebody.ClientTurnShape{
		Items: []largebody.ClientTurnItemShape{
			{
				Kind:    aipapi.ItemKindMessage,
				Role:    aipapi.RoleUser,
				Ordinal: 0,
				Parts: []largebody.ClientTurnPartShape{
					{Kind: aipapi.ContentPartText, ContentBytes: 42},
				},
			},
		},
		TotalContentBytes: 42,
	}

	recIn, err := prep.BuildClientTurnRecordInput(br, shape, 64*1024)
	if err != nil {
		t.Fatalf("BuildClientTurnRecordInput failed: %v", err)
	}
	if len(recIn.Lines) != 1 {
		t.Fatalf("Lines count = %d, want 1", len(recIn.Lines))
	}
	if recIn.Lines[0].Role != "user" || recIn.Lines[0].Ordinal != 0 || recIn.Lines[0].Parts[0] != "text" {
		t.Errorf("unexpected Lines[0]: %+v", recIn.Lines[0])
	}
}

// TestPayloadScaleIndependence_MemoryBounded verifies that MetadataBytes and the wire
// recorder input memory footprint do NOT scale with prompt text size (Requirements 14.5, 21.10).
func TestPayloadScaleIndependence_MemoryBounded(t *testing.T) {
	sizes := []int{100, 10_000, 100_000, 1_000_000, 5_000_000}
	var baselineMetaBytes int64

	now := time.Unix(1000, 0)
	br := app.BeginResult{
		Record: domain.Record{SessionID: "sess-scale"},
		TurnID: "turn-scale",
	}

	for i, sz := range sizes {
		largeText := strings.Repeat("A", sz)
		call := &aipapi.Call{
			Messages: []aipapi.Message{
				{
					Role: aipapi.RoleUser,
					Parts: []aipapi.Part{
						{Kind: aipapi.PartText, Text: largeText},
					},
				},
			},
		}

		shape, err := largebody.ClientTurnShapeFromCall(call, 64*1024)
		if err != nil {
			t.Fatalf("size %d: ClientTurnShapeFromCall failed: %v", sz, err)
		}

		metaBytes := shape.MetadataBytes()
		if i == 0 {
			baselineMetaBytes = metaBytes
		} else if metaBytes != baselineMetaBytes {
			t.Errorf("size %d: MetadataBytes = %d, want %d (should be independent of prompt size)", sz, metaBytes, baselineMetaBytes)
		}

		// TotalContentBytes reflects the byte length without retaining text
		if shape.TotalContentBytes != int64(sz) {
			t.Errorf("size %d: TotalContentBytes = %d, want %d", sz, shape.TotalContentBytes, sz)
		}

		recIn, err := BuildClientTurnRecordInputFromShape(now, "trace-scale", br, shape, 64*1024)
		if err != nil {
			t.Fatalf("size %d: BuildClientTurnRecordInputFromShape failed: %v", sz, err)
		}

		if len(recIn.Lines) != 1 || recIn.Lines[0].Parts[0] != "text" {
			t.Errorf("size %d: unexpected Lines: %+v", sz, recIn.Lines)
		}
	}
}
