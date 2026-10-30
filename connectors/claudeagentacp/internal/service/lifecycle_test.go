package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/acp"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// Requirements 2.7/5.3: crashes/auth failures preserve commitment, never retry
// inside the connector, and withdraw the failed process before another turn.
func TestService_LifecycleFailuresRespectOutputCommitment(t *testing.T) {
	for _, source := range []string{"api_key", "subscription_token", "host_login", "gateway"} {
		for _, crash := range []bool{false, true} {
			for _, after := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/crash=%v/after=%v", source, crash, after), func(t *testing.T) {
					svc, adapter, _, _ := serviceFixture(t, source)
					adapter.crash, adapter.promptError, adapter.outputBeforeError = crash, !crash, after
					inst := configureFixture(t, svc, source)
					stream, err := inst.eng.Open(t.Context(), turnCall())
					if err != nil {
						t.Fatal(err)
					}
					content, terminal := 0, 0
					for {
						ev, recvErr := stream.Recv(t.Context())
						if ev.Kind == aipapi.EventTextDelta || ev.Kind == aipapi.EventReasoningDelta {
							content++
						}
						if ev.Kind == aipapi.EventResponseFinished {
							terminal++
						}
						if recvErr != nil {
							err = recvErr
							break
						}
					}
					if err == nil || errors.Is(err, io.EOF) || aipapi.IsRecoverablePreOutput(err) == after || (content > 0) != after || terminal != 0 || adapter.starts != 1 {
						t.Fatalf("failure commitment: content=%d terminal=%d starts=%d err=%v", content, terminal, adapter.starts, err)
					}
					if strings.Contains(fmt.Sprintf("%+v %#v", err, err), "synthetic-private") {
						t.Fatal("credential-bearing failure")
					}
					if !crash {
						rpc, ok := errors.AsType[*acp.RPCError](err)
						remediation := map[string]string{"api_key": "api_key secret", "subscription_token": "claude setup-token", "host_login": "claude auth login", "gateway": "verify the gateway endpoint and provisioned header secrets"}[source]
						if !ok || rpc.Code != -32000 || rpc.Message != "" || !strings.Contains(err.Error(), remediation) {
							t.Fatalf("unsafe auth classification: %v", err)
						}
					}
					if err := stream.Close(); err != nil {
						t.Fatal(err)
					}
					if !adapter.processes[0].stopped() {
						t.Fatal("failed runtime released without joining process")
					}
					if source != "host_login" {
						requireRootGone(t, adapter.environments[0])
					}
					adapter.crash, adapter.promptError = false, false
					next, err := inst.eng.Open(t.Context(), turnCall())
					if err != nil {
						t.Fatal(err)
					}
					consume(t, next)
					if adapter.starts != 2 {
						t.Fatal("failed runtime reused")
					}
				})
			}
		}
	}
}

// Task 3.2 / requirement 5.3: option I/O and auth failures are not evidence
// of an unsupported model. Every stage withdraws before prompt and replacement
// replays history; the connector never retries the failed turn itself.
func TestService_OptionFailureWithdrawsAndResets(t *testing.T) {
	for _, source := range []string{"api_key", "subscription_token", "host_login", "gateway"} {
		for _, stage := range []string{"model", "request", "pooled-request", "pooled-reset"} {
			for _, auth := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/auth=%v", source, stage, auth), func(t *testing.T) {
					svc, adapter, _, _ := serviceFixture(t, source)
					adapter.handshakeMethod = "session/set_config_option"
					adapter.handshakeOption = map[string]int{"model": 1, "request": 2, "pooled-request": 3, "pooled-reset": 3}[stage]
					adapter.handshakeCrash, adapter.handshakeAuth = !auth, auth
					inst := configureFixture(t, svc, source)
					call := turnCall()
					call.Options.ReasoningEffort = "high"
					pooled := strings.HasPrefix(stage, "pooled-")
					if pooled {
						first, err := inst.eng.Open(t.Context(), call)
						if err != nil {
							t.Fatal(err)
						}
						consume(t, first)
						call.Messages = append(call.Messages, aipapi.Message{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("onetwo")}}, aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("second")}})
						if stage == "pooled-reset" {
							call.Options.ReasoningEffort = ""
						}
					}
					_, err := inst.eng.Open(t.Context(), call)
					proc := adapter.processes[0]
					wantPrompts := 0
					if pooled {
						wantPrompts = 1
					}
					if !aipapi.IsRecoverablePreOutput(err) || errors.Is(err, aipapi.ErrCapabilityReject) || !proc.stopped() || proc.prompts != wantPrompts || adapter.starts != 1 {
						t.Fatalf("option failure: prompts=%d starts=%d err=%v", proc.prompts, adapter.starts, err)
					}
					if strings.Contains(fmt.Sprintf("%+v %#v", err, err), "synthetic-private") {
						t.Fatal("option failure leaked credentials")
					}
					if auth {
						rpc, ok := errors.AsType[*acp.RPCError](err)
						remediation := map[string]string{"api_key": "api_key secret", "subscription_token": "claude setup-token", "host_login": "claude auth login", "gateway": "verify the gateway endpoint and provisioned header secrets"}[source]
						if !ok || rpc.Code != -32000 || rpc.Method != adapter.handshakeMethod || rpc.Message != "" || !strings.Contains(err.Error(), remediation) {
							t.Fatalf("option authentication metadata: %v", err)
						}
					}
					if source != "host_login" {
						requireRootGone(t, adapter.environments[0])
					}
					adapter.handshakeCrash, adapter.handshakeAuth = false, false
					next, err := inst.eng.Open(t.Context(), call)
					if err != nil {
						t.Fatal(err)
					}
					consume(t, next)
					if adapter.starts != 2 || adapter.processes[1].prompts != 1 {
						t.Fatal("failed option exchange reused process")
					}
					input := strings.Join(adapter.processes[1].inputs, "")
					if !strings.Contains(input, "hi") || (pooled && (!strings.Contains(input, "onetwo") || !strings.Contains(input, "second"))) {
						t.Fatal("replacement lost full history")
					}
				})
			}
		}
	}
}

func TestService_HandshakeFailureIsRecoverableAndWithdrawsRoot(t *testing.T) {
	for _, method := range []string{"initialize", "session/new"} {
		for _, auth := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/auth=%v", method, auth), func(t *testing.T) {
				svc, adapter, _, _ := serviceFixture(t, "api_key")
				adapter.handshakeCrash, adapter.handshakeAuth = !auth, auth
				adapter.handshakeMethod = method
				inst := configureFixture(t, svc, "api_key")
				_, err := inst.eng.Open(t.Context(), turnCall())
				if !aipapi.IsRecoverablePreOutput(err) || !adapter.processes[0].stopped() || adapter.processes[0].prompts != 0 {
					t.Fatalf("handshake failure: %v", err)
				}
				if strings.Contains(fmt.Sprintf("%+v %#v", err, err), "synthetic-private") {
					t.Fatal("handshake secret leaked")
				}
				if auth {
					rpc, ok := errors.AsType[*acp.RPCError](err)
					if !ok || rpc.Code != -32000 || rpc.Method != method || rpc.Message != "" || !strings.Contains(err.Error(), "api_key secret") {
						t.Fatalf("handshake classification: %v", err)
					}
				}
				requireRootGone(t, adapter.environments[0])
			})
		}
	}
}

// Requirement 5.2: an already-cancelled caller still sends session/cancel using
// the existing bounded cleanup context, then Close quiesces before pool release.
func TestService_CancelHasOneTerminalAndQuiesces(t *testing.T) {
	svc, adapter, _, _ := serviceFixture(t, "api_key")
	adapter.blockPrompt = true
	inst := configureFixture(t, svc, "api_key")
	stream, err := inst.eng.Open(t.Context(), turnCall())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	start := time.Now()
	result := stream.Cancel(ctx, aipapi.CancelCause{Kind: aipapi.CancelExplicit})
	if result.Mode != aipapi.CancelModeProvider || time.Since(start) > 3*time.Second {
		t.Fatalf("cancel: %+v", result)
	}
	terminals := 0
	for {
		ev, err := stream.Recv(t.Context())
		if ev.Kind == aipapi.EventResponseFinished {
			terminals++
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if terminals != 1 {
		t.Fatalf("terminal count %d", terminals)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	proc := adapter.processes[0]
	proc.mu.Lock()
	cancels := proc.cancels
	proc.mu.Unlock()
	if cancels != 1 || !proc.stopped() {
		t.Fatalf("cancel count=%d stopped=%v", cancels, proc.stopped())
	}
	requireRootGone(t, adapter.environments[0])
}

// Requirement 5.2 also holds through Execute's existing cancellation owner and
// ABI sequencer: one acknowledged cancel outcome, one cancelled terminal, joined
// process/root cleanup before returning. No connector-owned cancel goroutine.
func TestService_ExecuteCancelTerminalAndCleanup(t *testing.T) {
	svc, adapter, _, _ := serviceFixture(t, "api_key")
	adapter.blockPrompt = true
	inst := configureFixture(t, svc, "api_key")
	inv := backendplugin.Invocation{RequestID: "cancel", AttemptID: "attempt", ALegID: "a", BLegID: "b", NativeModelID: "future-family-17-3", CanonicalModelID: "anthropic/future-family-17.3", ProxyOwnedSessionID: "client", Messages: []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{wireText("hi")}}}}
	wire := &executeCapture{ctx: t.Context(), invocation: &inv, negotiated: true, cancelTurn: true}
	if err := inst.Execute(wire); err != nil {
		t.Fatal(err)
	}
	terminals, outcomes := 0, 0
	for _, frame := range wire.frames {
		if frame.Terminal != nil {
			terminals++
			if frame.Terminal.Status != backendplugin.TerminalCancelled {
				t.Fatalf("terminal: %+v", frame.Terminal)
			}
		}
		if frame.CancelOutcome != nil {
			outcomes++
			if !frame.CancelOutcome.Acknowledged || frame.CancelOutcome.Mode != aipapi.CancelModeProvider {
				t.Fatalf("outcome: %+v", frame.CancelOutcome)
			}
		}
	}
	proc := adapter.processes[0]
	proc.mu.Lock()
	cancels := proc.cancels
	proc.mu.Unlock()
	if terminals != 1 || outcomes != 1 || cancels != 1 || !proc.stopped() {
		t.Fatalf("terminal/outcome/cancel/process: %d/%d/%d/%v", terminals, outcomes, cancels, proc.stopped())
	}
	requireRootGone(t, adapter.environments[0])
}

// Execute's existing deadline owner must force Close and join a cancel writer
// blocked on an unresponsive peer; it must not leave a worker/process behind.
func TestService_ExecuteCancelDeadlineQuiescesUnresponsivePeer(t *testing.T) {
	svc, adapter, _, _ := serviceFixture(t, "api_key")
	adapter.blockPrompt, adapter.stallCancel = true, true
	inst := configureFixture(t, svc, "api_key")
	inv := backendplugin.Invocation{RequestID: "deadline", AttemptID: "attempt", ALegID: "a", BLegID: "b", NativeModelID: "future-family-17-3", CanonicalModelID: "anthropic/future-family-17.3", Messages: []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{wireText("hi")}}}}
	wire := &executeCapture{ctx: t.Context(), invocation: &inv, negotiated: true, cancelTurn: true}
	start := time.Now()
	if err := inst.Execute(wire); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("cancel ignored the Execute deadline")
	}
	terminals := 0
	for _, frame := range wire.frames {
		if frame.Terminal != nil {
			terminals++
			if frame.Terminal.Status != backendplugin.TerminalCancelled {
				t.Fatal("deadline became success")
			}
		}
	}
	if terminals != 1 || !adapter.processes[0].stopped() {
		t.Fatal("deadline left terminal/process ownership unresolved")
	}
	requireRootGone(t, adapter.environments[0])
}

// Identity/options failure must be withdrawn under the original turn claim.
func TestService_MalformedAdmissionWithdrawsRootBeforeReuse(t *testing.T) {
	for _, options := range []bool{false, true} {
		t.Run(fmt.Sprint(options), func(t *testing.T) {
			svc, adapter, _, _ := serviceFixture(t, "api_key")
			adapter.malformedOptions, adapter.malformedIdentity = options, !options
			adapter.failAfterTurn = true
			inst := configureFixture(t, svc, "api_key")
			first, err := inst.eng.Open(t.Context(), turnCall())
			if err != nil {
				t.Fatal(err)
			}
			consume(t, first)
			call := turnCall()
			call.Messages = append(call.Messages, aipapi.Message{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("onetwo")}}, aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("second")}})
			_, err = inst.eng.Open(t.Context(), call)
			if err == nil || !adapter.processes[0].stopped() || adapter.processes[0].prompts != 1 {
				t.Fatalf("malformed admission: %v", err)
			}
			requireRootGone(t, adapter.environments[0])
			adapter.malformedOptions, adapter.malformedIdentity = false, false
			next, err := inst.eng.Open(t.Context(), call)
			if err != nil {
				t.Fatal(err)
			}
			consume(t, next)
			if adapter.starts != 2 {
				t.Fatal("ambiguous transport reused")
			}
			proc := adapter.processes[1]
			proc.mu.Lock()
			input := strings.Join(proc.inputs, "")
			proc.mu.Unlock()
			if !strings.Contains(input, "hi") || !strings.Contains(input, "onetwo") || !strings.Contains(input, "second") {
				t.Fatal("replacement did not replay the full history")
			}
		})
	}
}

// Requirements 5.1/5.4: edited history and expired idle entries use a new root,
// not a new handle around the previous physical process.
func TestService_DivergenceAndIdleReapReplaceOwnedRoot(t *testing.T) {
	for _, idle := range []bool{false, true} {
		t.Run(fmt.Sprint(idle), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				svc, adapter, _, _ := serviceFixture(t, "api_key")
				extra := ""
				if idle {
					extra = "idle_timeout_seconds: 1\n"
				}
				inst := configureFixture(t, svc, "api_key", extra)
				first, err := inst.eng.Open(t.Context(), turnCall())
				if err != nil {
					t.Fatal(err)
				}
				consume(t, first)
				call := turnCall()
				if idle {
					time.Sleep(2 * time.Second)
				} else {
					call.Messages[0].Parts = []aipapi.Part{aipapi.TextPart("edited")}
				}
				next, err := inst.eng.Open(t.Context(), call)
				if err != nil {
					t.Fatal(err)
				}
				consume(t, next)
				if adapter.starts != 2 || !adapter.processes[0].stopped() {
					t.Fatal("old process survived replacement")
				}
				requireRootGone(t, adapter.environments[0])
				if environmentValue(adapter.environments[0], "HOME") == environmentValue(adapter.environments[1], "HOME") {
					t.Fatal("replacement reused root")
				}
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				if err := inst.Close(ctx); err != nil {
					t.Fatal(err)
				}
				if !adapter.processes[1].stopped() {
					t.Fatal("cancelled Close abandoned cleanup")
				}
				requireRootGone(t, adapter.environments[1])
				if _, err := os.Stat(inst.eng.stateDir); !os.IsNotExist(err) {
					t.Fatal("state root survives Close")
				}
				if _, err := inst.eng.Open(t.Context(), call); err == nil || adapter.starts != 2 {
					t.Fatal("closed engine admitted a process")
				}
			})
		})
	}
}

func requireRootGone(t *testing.T, env []string) {
	t.Helper()
	root := filepath.Dir(environmentValue(env, "HOME"))
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("owned process root survives: %v", err)
	}
}
