package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"
)

type refusalSpec struct {
	negotiatedOptionsSpec
	strict bool
	auto   bool
	ready  <-chan struct{}
}

func (s refusalSpec) TransportOptions() TransportOptions {
	return TransportOptions{RefuseOutOfTurnRequests: s.strict, AutoApproveOutOfTurnPermissions: s.auto}
}

func (s refusalSpec) HandshakeProfile() HandshakeProfile {
	<-s.ready
	return s.negotiatedOptionsSpec.HandshakeProfile()
}

// A distinguishable active reply proves the reader delegates only while a
// prompt is active. Claude's actual decline selection is tested in its module.
func (s refusalSpec) ServerRequestHandler() ServerRequestHandler {
	return activePermissionHandler{}
}

type activePermissionHandler struct{}

func (activePermissionHandler) HandleServerRequest(_ context.Context, method string, _, _ json.RawMessage) (any, error) {
	if method != "session/request_permission" {
		return nil, errors.New("method not found")
	}
	return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": "reject"}}, nil
}

type permissionStarter struct{ proc Process }

func (s permissionStarter) Start([]string, string, []string) (Process, error) {
	return s.proc, nil
}

// Requests block the fake agent until it receives a nonempty refusal. This
// exercises the construction seam and real unary/prompt demultiplexing rather
// than calling routeLine directly.
func TestTransportPermissions_RefuseAcrossLifetime(t *testing.T) {
	for _, tc := range []struct{ strict, auto bool }{{true, false}, {false, false}, {false, true}, {true, true}} {
		t.Run(fmt.Sprintf("refuse=%t/approve=%t", tc.strict, tc.auto), func(t *testing.T) {
			proc := newFakeProcess(t)
			ready := make(chan struct{})
			turns := make(chan struct{})
			spec := refusalSpec{strict: tc.strict, auto: tc.auto, ready: ready}
			engine := NewSubprocessBackend(SubprocessBackendConfig{
				Protocol: NewACPProtocol(spec, nil), ProcessStarter: permissionStarter{proc},
			})
			backend, ok := engine.(*subprocessBackend)
			if !ok {
				t.Fatal("unexpected backend type")
			}
			defer func() { _ = backend.Close() }()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			// A timeout kills the pipes too, bounding a broken reader/write schedule.
			stop := context.AfterFunc(ctx, func() { _ = proc.Kill() })
			defer stop()
			done := make(chan error, 1)
			go func() {
				scanner := bufio.NewScanner(proc.stdinR)
				read := func() (map[string]json.RawMessage, error) {
					if !scanner.Scan() {
						return nil, fmt.Errorf("stdin ended: %v", scanner.Err())
					}
					var message map[string]json.RawMessage
					err := json.Unmarshal(scanner.Bytes(), &message)
					return message, err
				}
				refuse := func(phase string, active bool) error {
					for i, method := range []string{"session/request_permission", "session/request_permission", "unknown/method"} {
						once := `,{"optionId":"yes","kind":"allow_once"}`
						if i == 1 {
							once = ""
						}
						if _, err := fmt.Fprintf(proc.stdoutW, `{"jsonrpc":"2.0","id":"server","method":%q,"params":{"options":[{"optionId":"persist","kind":"allow_always"},{"optionId":"reject","kind":"reject_once"}%s]}}`+"\n", method, once); err != nil {
							return err
						}
						reply, err := read()
						if err != nil {
							return err
						}
						if string(reply["id"]) != `"server"` {
							return fmt.Errorf("%s: wrong reply id: %s", phase, reply["id"])
						}
						if !tc.strict && !tc.auto && !active {
							if string(reply["result"]) != `{}` {
								return fmt.Errorf("legacy reply changed: %v", reply)
							}
						} else if method == "session/request_permission" {
							want := `{"outcome":{"outcome":"cancelled"}}`
							if active {
								want = `{"outcome":{"optionId":"reject","outcome":"selected"}}`
							} else if tc.auto && !tc.strict && i != 1 {
								want = `{"outcome":{"optionId":"yes","outcome":"selected"}}`
							}
							if string(reply["result"]) != want {
								return fmt.Errorf("%s: permission reply: %s want %s", phase, reply["result"], want)
							}
						} else {
							var rpcErr struct {
								Code int `json:"code"`
							}
							if err := json.Unmarshal(reply["error"], &rpcErr); err != nil || rpcErr.Code != -32601 {
								return fmt.Errorf("%s: unknown request not rejected: %v", phase, reply)
							}
						}
					}
					return nil
				}
				if err := refuse("before initialize", false); err != nil {
					close(ready)
					done <- err
					return
				}
				close(ready)
				for _, method := range []string{"initialize", "session/new", "session/set_config_option", "session/set_config_option", "session/prompt", "session/prompt"} {
					req, err := read()
					if err != nil {
						done <- err
						return
					}
					if string(req["method"]) != fmt.Sprintf("%q", method) {
						done <- fmt.Errorf("want %s, got %s", method, req["method"])
						return
					}
					if err := refuse("during "+method, method == "session/prompt"); err != nil {
						done <- err
						return
					}
					result := `{}`
					switch method {
					case "initialize":
						result = `{"protocolVersion":1}`
					case "session/new":
						result = `{"sessionId":"sid","configOptions":` + advertisedOptions("opus", "low", "low") + `}`
					case "session/set_config_option":
						result = `{"configOptions":` + advertisedOptions("sonnet", "medium", "medium", "high") + `}`
					case "session/prompt":
						result = `{"stopReason":"end_turn"}`
					}
					if _, err := fmt.Fprintf(proc.stdoutW, `{"jsonrpc":"2.0","id":%s,"result":%s}`+"\n", req["id"], result); err != nil {
						done <- err
						return
					}
					if method == "session/prompt" {
						if err := refuse("after terminal / between turns", false); err != nil {
							done <- err
							return
						}
						select {
						case turns <- struct{}{}:
						case <-ctx.Done():
							done <- ctx.Err()
							return
						}
					}
				}
				done <- nil
			}()
			res, _, err := backend.ensureProcess(ctx, RuntimeKey{Model: "sonnet"}, "sonnet", "", "", "")
			if err != nil {
				select {
				case agentErr := <-done:
					t.Fatalf("%v; agent: %v", err, agentErr)
				default:
					t.Fatal(err)
				}
			}
			// One option RPC outside the prompt, then two turns on the same transport.
			if _, err := res.Transport.CallUnary(ctx, []byte(`{"jsonrpc":"2.0","id":50,"method":"session/set_config_option"}`), 200); err != nil {
				t.Fatal(err)
			}
			for _, id := range []int{60, 61} {
				body, err := res.Transport.CallPromptStream(ctx, fmt.Appendf(nil, `{"jsonrpc":"2.0","id":%d,"method":"session/prompt"}`, id))
				if err != nil {
					t.Fatal(err)
				}
				stream := newPromptNDJSONStream(ctx, body, newClientFromTransport(res.Transport, nil), "sid", int64(id), "", SessionUpdateMapperOptions{}, spec.ServerRequestHandler(), CancelProfile{}, 0)
				for {
					_, err := stream.Recv(ctx)
					if errors.Is(err, io.EOF) {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				_ = stream.Close()
				select {
				case <-turns:
				case err := <-done:
					t.Fatalf("agent: %v", err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		})
	}
}
