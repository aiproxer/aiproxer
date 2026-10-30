package acp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// The existing pool withdraws this runtime before releasing its turn claim.
type promptAdmissionFailure struct{ cause error }

func (e *promptAdmissionFailure) Error() string { return "acp: prompt admission refused" }
func (e *promptAdmissionFailure) Unwrap() error { return e.cause }
func isPromptAdmissionFailure(err error) bool {
	var failure *promptAdmissionFailure
	return errors.As(err, &failure)
}

func admitPrompt(ctx context.Context, c *client, sessionID string, spec PromptAdmissionSpec) (json.RawMessage, error) {
	transport, ok := c.t.(sessionOptionsTransport)
	if !ok {
		return nil, &promptAdmissionFailure{}
	}
	store := transport.acpSessionOptions()
	previous := store.current.Load()
	fail := func() (json.RawMessage, error) {
		store.current.Store(nil)
		return nil, &promptAdmissionFailure{cause: ctx.Err()}
	}
	if previous == nil || previous.sessionID != sessionID || store.ended.Load() || spec.PromptAdmissionMethod() == "" || spec.PromptAdmissionMetadataKey() == "" {
		return fail()
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// Unary writes can block on a full pipe. Cancellation withdraws the existing
	// transport/tree authority, then joins this callback before returning to the
	// pool's history-reset/release path. No second subprocess owner is introduced.
	closed := make(chan struct{})
	stopClose := context.AfterFunc(ctx, func() { _ = c.t.Close(); close(closed) })
	defer func() {
		if !stopClose() {
			<-closed
		}
	}()
	var nonceBytes [32]byte
	if _, err := rand.Read(nonceBytes[:]); err != nil {
		return fail()
	}
	nonce := hex.EncodeToString(nonceBytes[:])
	params, err := json.Marshal(map[string]string{"sessionId": sessionID, "checkpointId": nonce})
	if err != nil {
		return fail()
	}
	id := jsonRPCNumericID(c.rpcID())
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: id, Method: spec.PromptAdmissionMethod(), Params: params})
	if err != nil {
		return fail()
	}
	raw, err := c.t.CallUnary(ctx, body, 200)
	if err != nil {
		return fail()
	}
	var response rpcResponse
	if json.Unmarshal(raw, &response) != nil || response.JSONRPC != "2.0" || string(response.ID) != string(id) || response.Error != nil || ctx.Err() != nil {
		return fail()
	}
	if err := spec.ValidatePromptAdmission(sessionID, nonce, previous.admission, response.Result); err != nil {
		return fail()
	}
	next := *previous
	next.admission = append(json.RawMessage(nil), response.Result...)
	if !store.publish(previous, &next) {
		return fail()
	}
	return append(json.RawMessage(nil), next.admission...), nil
}

func failedAdmittedPrompt(err error) error {
	// Do not expose agent errors or identity payloads through diagnostics.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &promptAdmissionFailure{cause: err}
	}
	return &promptAdmissionFailure{cause: fmt.Errorf("prompt transport failed")}
}
