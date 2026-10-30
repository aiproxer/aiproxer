package service

// The direct factory's execution path.
//
// One canonical attempt is served through the public SIWC Responses route:
//
//  1. the selected registration is read from the local authority ONCE and
//     checked for usability and the direct plan-use grant BEFORE any provider
//     I/O; that registration is the attempt's BINDING;
//  2. the 4.3 preview policy admits the call under the operator's posture and
//     produces the exact public request plus every explicit omission;
//  3. the bound registration's credential is acquired, and the record that
//     supplied it is re-validated BEFORE provider I/O: a registration replaced
//     with a scope-less one, or a selection that moved, fails the attempt;
//  4. the 5.1 HTTP/SSE client sends exactly the bound credential to the
//     documented public endpoint and always streams (`store:false`,
//     `stream:true`);
//  5. the 5.2 normalizer turns provider events into canonical events and
//     reports EOF only after the `response.completed` terminal.
//
// # One bound identity per attempt
//
// The selection is an authority, and it may move while an attempt is being
// prepared. This adapter therefore does not read it twice and hope the two
// reads agree: the id read at entry is used for the admission decision AND for
// the credential acquisition, the record that supplied the token is validated
// as that same registration, and the selection is re-read only to REFUSE an
// attempt whose registration moved. The transport receives the bound credential
// and never re-reads the selection for the attempt.
//
// # One provider path for both downstream modes
//
// The downstream delivery mode is not read here. A downstream streaming client
// receives the canonical events as they arrive, and a downstream non-streaming
// client is served by the host's collection over the same canonical stream.
// There is deliberately no provider non-stream request path to select: the
// public route has one transport posture, and the connector keeps it.
//
// # No transparent replay
//
// The normalizer marks client-visible output as committed and refuses a
// transparent replay (ErrReplayAfterVisibleOutput). This adapter never retries
// an attempt: a provider failure after visible output fails the call, and core
// owns any policy decision about what to do next. A retry never happens in the
// connector, so it cannot happen unknowingly.
//
// # Cancellation
//
// The provider request is opened under a child context owned by the managed
// stream. A cancelled downstream context propagates through the request, and
// an explicit CANCEL frame aborts it through Cancel, which releases the HTTP
// connection. Content already delivered stays delivered; nothing is replayed.
//
// # Accounting sideband
//
// The completed stream's usage is bridged to the negotiated V1 accounting
// sideband once per completed stream, through the existing backend-plugin usage
// evidence buffer. Every usage delta is collected and ONE record is emitted at
// completion. It carries token counters only, with the provider-reported source
// and authoritative authority; no price, rating or other money notion is
// manufactured for subscription-funded usage. The dedupe key identifies the one
// provider charge (the provider correlation identity of the answer, taken before
// its diagnostic display bound) and the non-secret local profile id that
// authorized it, so a redelivery stays one charge and two charges never merge,
// without carrying a credential.

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/responses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// ErrExecutionUnavailable reports an instance that cannot run an attempt
// because it was configured without a registration store.
//
// The store is the only authority that can name a selected registration and
// supply its credential, so an instance without one has no account to act for.
// The failure is explicit and pre-I/O: a fabricated empty stream, an anonymous
// request or another profile's credential would all be worse than a visible
// refusal.
var ErrExecutionUnavailable = fmt.Errorf(
	"openai-chatgpt-plan: no registration store is configured, so no ChatGPT-plan registration can authorize an attempt: %w",
	responses.ErrNoSelectedProfile,
)

// ErrSelectedRegistrationUnusable reports a selected registration that cannot
// authorize an attempt: it could not be read, carries no credential, needs a
// new authorization, or does not have the direct plan-use scope.
//
// The failure is a LOCAL discriminator, distinct from any provider recovery
// code: the attempt never reached the provider, so there is no provider status,
// code or request id to report. It is checked per call, because the selection
// is an authority and not a value copied at Configure.
var ErrSelectedRegistrationUnusable = fmt.Errorf(
	"openai-chatgpt-plan: the selected registration cannot authorize a ChatGPT-plan attempt: %w",
	responses.ErrRegistrationUnusable,
)

// usageDedupeKeyPrefix and the charge identity live in usage_evidence.go:
// the dedupe key names ONE provider charge, never just the account.

// execution is the configured provider execution of one direct factory
// instance.
//
// It holds the transport client and the registration authority it authorizes
// with; it holds no credential of its own, and it keeps no selection. The
// selection is read on every call, ONCE, and bound to that attempt: a
// registration replaced with a valid credential is the credential the attempt
// sends, while a selection that moves to another registration refuses the
// in-flight attempt and the next attempt starts from the new selection.
type execution struct {
	client    *responses.Client
	authority *selectedProfiles
	// endpoint is the exact Responses endpoint this execution sends to.
	//
	// It is the documented public resource. The only other value the
	// construction seam accepts is the loopback reference endpoint of the
	// TEST-ONLY constructor (NewWithReferenceEndpoint), which the connector's
	// tests and the staged host integration use; the operator configuration
	// cannot name an endpoint.
	endpoint   string
	policyMode responses.PolicyMode
	// accountingEvidenceV1 reports whether the negotiated ABI session carries
	// the V1 accounting evidence sideband. An older host that did not
	// negotiate it must never receive an accounting frame.
	accountingEvidenceV1 bool
}

// newExecutionFromConfig builds the provider execution of one instance.
//
// A configuration without a registration store builds no execution: the
// instance then refuses every attempt explicitly instead of reading a store the
// operator did not configure.
//
// referenceBaseURL is the TEST-ONLY reference seam. It is supplied by
// NewWithReferenceEndpoint and is EMPTY for every production construction:
// Configure parses no endpoint key, so the shipped surface always fixes the
// documented public resource.
func newExecutionFromConfig(
	cfg Config,
	authority *selectedProfiles,
	negotiation backendplugin.Negotiation,
	referenceBaseURL string,
) (*execution, error) {
	if authority == nil {
		return nil, nil
	}
	endpoint := responses.PublicBaseURL + responses.ResponsesPath
	if reference := strings.TrimSpace(referenceBaseURL); reference != "" {
		validated, err := responses.EndpointForBaseURL(reference)
		if err != nil {
			return nil, fmt.Errorf("openai-chatgpt-plan: the test reference endpoint is unusable: %w", err)
		}
		endpoint = validated
	}
	client, err := responses.NewClient(responses.ClientConfig{Tokens: authority})
	if err != nil {
		return nil, fmt.Errorf("openai-chatgpt-plan: build the Responses transport: %w", err)
	}
	return &execution{
		client:               client,
		authority:            authority,
		endpoint:             endpoint,
		policyMode:           cfg.PolicyMode,
		accountingEvidenceV1: backendplugin.AccountingEvidenceNegotiated(negotiation),
	}, nil
}

// Execute runs one canonical attempt against the public SIWC Responses API.
//
// The shared backend-plugin execute helper owns the frame protocol, the
// cancellation handshake and the accounting sideband drain; this method
// supplies the provider stream. A failure is emitted through the standard
// terminal path, so the host observes one failed attempt rather than a stream
// that ends without a verdict, and the returned error carries this connector's
// typed recovery class.
func (i *instance) Execute(stream backendplugin.ExecuteStream) error {
	return classifyAttemptOutcome(backendplugin.ForwardExecute(stream, i.openManagedStream))
}

// classifyAttemptOutcome gives the host this connector's typed recovery class
// for one failed attempt.
//
// Classification must happen before ForwardExecute publishes the terminal: a
// classified error implements the shared TerminalError seam, and the helper
// uses it to publish the recovery class and safe retryability instead of the
// generic internal terminal. The provider's observed status, machine-readable
// code and request id, the bounded diagnostic and the non-secret local profile
// id stay on the classification, so the failure is explicit rather than a
// silent empty result.
//
// The classification changes WHO recovers, never HOW MANY attempts this
// connector makes: exactly one provider request was sent, and there is no
// retry, no model substitution and no registration rotation to enable. The
// class only states what the host may do with a LATER attempt - the named
// plan-limit outcome is terminal for the selected registration, while a
// temporary condition may be retried against the same one.
//
// A caller cancellation and an error outside the connector's failure surface
// are returned unchanged: neither is a recovery class this connector owns.
func classifyAttemptOutcome(err error) error {
	if err == nil {
		return nil
	}
	classified, ok := responses.Classify(err)
	if !ok {
		return err
	}
	return classified
}

// classifyBeforeTerminal classifies a failure that happens before the provider
// stream exists. The shared forwarder publishes the terminal from the error it
// receives, so the classification cannot wait for the return from Execute.
func classifyBeforeTerminal(err error) error {
	if err == nil {
		return nil
	}
	classified, ok := responses.Classify(err)
	if !ok {
		return err
	}
	return classified
}

// openManagedStream authorizes and opens one attempt.
//
// The order is the contract: the selected registration is bound and checked
// first, then the preview policy admits the call, and only then is the bound
// registration's credential acquired and the provider asked. An unusable
// selection, a selection that moved, a replaced credential without the plan-use
// grant, or an inadmissible call therefore fails with ZERO provider I/O.
func (i *instance) openManagedStream(
	ctx context.Context,
	inv backendplugin.Invocation,
	call aipapi.Call,
) (aipapi.ManagedEventStream, error) {
	var exec *execution
	if i != nil {
		exec = i.execution
	}
	if exec == nil {
		return nil, classifyBeforeTerminal(ErrExecutionUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// CHECK, do not assume: the selection and the registration's usability are
	// read NOW, on every call, and BOUND to this attempt. Configure does not
	// copy either value, and the transport does not read them again later.
	binding, err := exec.bindAttempt(ctx)
	if err != nil {
		return nil, classifyBeforeTerminal(err)
	}

	admission, err := responses.AdmitCall(responses.ProjectInput{
		Call:  call,
		Model: nativeModelID(inv.CanonicalModelID),
	}, exec.policyMode)
	if err != nil {
		return nil, classifyBeforeTerminal(err)
	}
	recordOmissions(ctx, inv.RequestID, binding.profileID, admission.Omissions)

	// The credential is acquired from the BOUND registration, and the record
	// that supplied it is validated before any request exists.
	credential, err := exec.acquireCredential(ctx, inv.RequestID, binding)
	if err != nil {
		return nil, classifyBeforeTerminal(err)
	}

	request := admission.Request
	request.URL = exec.endpoint
	requestCtx, cancel := context.WithCancel(ctx)
	raw, err := exec.client.OpenStreamForAttempt(requestCtx, request, credential)
	if err != nil {
		cancel()
		return nil, classifyBeforeTerminal(err)
	}
	return newManagedStream(raw, cancel, credential.ProfileID, inv.AttemptID, exec.accountingEvidenceV1), nil
}

// attemptBinding is the ONE registration an attempt is authorized to act for.
//
// It is resolved from the selection ONCE and validated, and then used for both
// the admission decision and the credential acquisition. The mutable selection
// is never consulted again to CHOOSE a credential: it is re-read only to refuse
// an attempt whose registration moved while it was being prepared.
type attemptBinding struct {
	profileID string
}

// bindAttempt resolves and validates the attempt's registration.
func (e *execution) bindAttempt(ctx context.Context) (attemptBinding, error) {
	profileID, err := e.authority.Selected(ctx)
	if err != nil {
		return attemptBinding{}, fmt.Errorf("%w: read the selected registration: %w", ErrSelectedRegistrationUnusable, err)
	}
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return attemptBinding{}, fmt.Errorf("%w: no registration is selected", ErrSelectedRegistrationUnusable)
	}
	if err := e.validateRegistration(profileID); err != nil {
		return attemptBinding{}, err
	}
	return attemptBinding{profileID: profileID}, nil
}

// validateRegistration proves that a stored registration can authorize plan
// usage before any request is opened for it.
func (e *execution) validateRegistration(profileID string) error {
	record, err := e.authority.Load(profileID)
	if err != nil {
		return fmt.Errorf("%w: load the selected registration %q: %w", ErrSelectedRegistrationUnusable, profileID, err)
	}
	if err := inventoryUnusableReason(record); err != nil {
		return fmt.Errorf("%w: %w", ErrSelectedRegistrationUnusable, err)
	}
	return nil
}

// acquireCredential acquires the bound registration's credential and proves,
// BEFORE provider I/O, that the record which SUPPLIED it is still that
// registration, still carries the direct plan-use grant, and is still the
// selection.
//
// The grant names the credential pair the token was acquired from. The stored
// record must still carry that same pair and must still pass the same checks
// the attempt entered with: a registration replaced while the token was
// acquired is judged as the record that would actually be sent, not as the one
// admission saw. The final selection read is a REFUSAL check only - its answer
// never chooses the credential.
func (e *execution) acquireCredential(
	ctx context.Context,
	requestID string,
	binding attemptBinding,
) (responses.BoundCredential, error) {
	grant, err := e.authority.Token(ctx, binding.profileID)
	if err != nil {
		return responses.BoundCredential{}, fmt.Errorf(
			"%w: acquire the credential of the registration %q: %w",
			ErrSelectedRegistrationUnusable, binding.profileID, err,
		)
	}
	if strings.TrimSpace(grant.AccessToken) == "" {
		return responses.BoundCredential{}, fmt.Errorf(
			"%w: the registration %q supplied no credential",
			ErrSelectedRegistrationUnusable, binding.profileID,
		)
	}
	record, err := e.authority.Load(binding.profileID)
	if err != nil {
		return responses.BoundCredential{}, fmt.Errorf(
			"%w: load the registration that supplied the credential %q: %w",
			ErrSelectedRegistrationUnusable, binding.profileID, err,
		)
	}
	if err := inventoryUnusableReason(record); err != nil {
		return responses.BoundCredential{}, fmt.Errorf(
			"%w: the record that supplied the credential cannot authorize the attempt: %w",
			ErrSelectedRegistrationUnusable, err,
		)
	}
	generation, ok := record.CredentialGeneration()
	if !ok || generation != grant.Generation {
		return responses.BoundCredential{}, fmt.Errorf(
			"%w: the registration %q changed while its credential was acquired",
			ErrSelectedRegistrationUnusable, binding.profileID,
		)
	}
	selected, err := e.authority.Selected(ctx)
	if err != nil {
		return responses.BoundCredential{}, fmt.Errorf(
			"%w: read the selected registration after the credential was acquired: %w",
			ErrSelectedRegistrationUnusable, err,
		)
	}
	if selected = strings.TrimSpace(selected); selected != binding.profileID {
		// Operator-visible: the attempt is refused BEFORE provider I/O, and
		// the record names both the bound identity and the identity that
		// replaced it, so the refusal can be explained without guessing which
		// registration would have been charged.
		slog.WarnContext(ctx, "openai-chatgpt-plan: the selected registration changed during an attempt",
			"request_id", requestID,
			"bound_profile_id", binding.profileID,
			"selected_profile_id", selected,
		)
		return responses.BoundCredential{}, fmt.Errorf(
			"%w: the selected registration changed from %q to %q during the attempt",
			ErrSelectedRegistrationUnusable, binding.profileID, selected,
		)
	}
	// The attempt is served under exactly this identity. The record is the
	// operator's audit trail: the credential that will be sent belongs to it.
	slog.InfoContext(ctx, "openai-chatgpt-plan: attempt bound to the selected registration",
		"request_id", requestID,
		"profile_id", binding.profileID,
	)
	return responses.BoundCredential{
		ProfileID:   binding.profileID,
		AccessToken: grant.AccessToken,
		Generation:  grant.Generation,
	}, nil
}

// nativeModelID strips the connector's route prefix from the canonical model.
//
// The host names the model by its canonical id; the provider knows the native
// slug. Only this connector's own prefix is removed, and a model that does not
// carry it is forwarded as it arrived.
func nativeModelID(canonical string) string {
	model := strings.TrimSpace(canonical)
	if prefix := FactoryKind + "/"; strings.HasPrefix(model, prefix) {
		return strings.TrimSpace(strings.TrimPrefix(model, prefix))
	}
	return model
}

// recordOmissions records every explicit preview-policy degradation.
//
// The event is the operator's observability seam: a value the public route
// cannot carry is either refused or omitted with a stable field and reason, and
// an omission that is not recorded here would be a hidden degradation. The
// record names no provider content and no credential.
func recordOmissions(ctx context.Context, requestID, profileID string, omissions []responses.Omission) {
	if len(omissions) == 0 {
		return
	}
	logger := slog.Default()
	for _, omission := range omissions {
		logger.WarnContext(ctx, "openai-chatgpt-plan: request degraded under the SIWC preview policy",
			"request_id", requestID,
			"profile_id", profileID,
			"field", omission.Field,
			"wire_field", omission.Wire,
			"reason", string(omission.Reason),
		)
	}
}

// managedStream is the canonical, managed stream of one provider answer.
//
// It forwards the normalized canonical events unchanged, owns the request
// context that aborts the upstream request on cancellation, and bridges the
// completed stream's usage to the accounting sideband exactly once.
type managedStream struct {
	inner     *responses.NormalizedStream
	cancel    context.CancelFunc
	usage     *backendplugin.UsageEvidenceBuffer
	dedupeKey string
	// profileID is the non-secret local registration that authorized the
	// attempt. It is the record's provenance, repeated in the operator log.
	profileID string
	// totals is the whole stream's accumulated provider-reported usage. The
	// evidence record is built from it at completion, never from the first
	// delta.
	totals usageTotals
	// identitySource names where the charge identity came from, for the
	// operator-visible record.
	identitySource string
	// emitted records that the one sideband record of this stream exists.
	emitted bool
}

// newManagedStream wraps one raw provider answer.
//
// The accounting provenance names the attempt's BOUND registration: the
// transport was handed that same identity with the credential it sent, so the
// evidence cannot describe a registration other than the one that authorized
// the request. The provider's correlation identity - the FULL value from the
// answer envelope, before its diagnostic display bound - names the charge
// itself.
func newManagedStream(raw *responses.Stream, cancel context.CancelFunc, profileID, attemptID string, accountingEvidenceV1 bool) *managedStream {
	attributed := strings.TrimSpace(profileID)
	buffer := backendplugin.NewUsageEvidenceBuffer()
	buffer.SetEnabled(accountingEvidenceV1)
	identity, source := chargeIdentity(raw.RequestIdentity(), attemptID)
	return &managedStream{
		inner:          raw.Normalize(),
		cancel:         cancel,
		usage:          buffer,
		dedupeKey:      usageDedupeKey(attributed, identity),
		profileID:      attributed,
		identitySource: source,
	}
}

// Recv returns the next canonical event.
//
// The usage evidence is captured on the way through, so the sideband record
// exists by the time the forwarding coordinator drains it after this event.
func (s *managedStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if s == nil || s.inner == nil {
		return aipapi.Event{}, io.EOF
	}
	event, err := s.inner.Recv(ctx)
	if err != nil {
		// Classify before the shared forwarder publishes the terminal: the
		// terminal mapping is a TerminalError on the classified error, and the
		// post-classification wrapper in Execute would run too late.
		if classified, ok := responses.Classify(err); ok {
			return event, classified
		}
		return event, err
	}
	s.captureUsage(ctx, event)
	return event, nil
}

// captureUsage collects the stream's usage and emits its ONE record at
// completion.
//
// Every usage delta is accumulated; the record is built when the normalized
// stream reports the completed terminal, so the evidence describes the COMPLETE
// stream rather than its first delta. Only a completed stream is charged: a
// failed or cancelled stream never yields the completion event, so it emits no
// usage and no counter is invented.
//
// The completed-only rule is CHECKED, not assumed: the normalizer's success path
// is the only finish this connector produces today and it always reports
// "completed", so a future producer that emitted another finish shape must not
// turn an uncharged outcome into provider usage.
func (s *managedStream) captureUsage(ctx context.Context, event aipapi.Event) {
	if s == nil || s.usage == nil {
		return
	}
	switch event.Kind {
	case aipapi.EventUsageDelta:
		s.totals.add(event)
	case aipapi.EventResponseFinished:
		if event.ResponseStatus != responseStatusCompleted {
			return
		}
		s.emitUsage(ctx)
	}
}

// emitUsage bridges the completed stream's whole usage to the V1 sideband.
//
// The record is built on a COPY: the canonical event delivered to the client is
// not rewritten, and the accounting plane/source/authority are added only where
// the host-side accounting contract reads them. At most one record is handed to
// the sideband buffer per stream, and only when the provider actually reported
// usage. Handing over is not delivery: the buffer owns acceptance and drops the
// record when accounting evidence was not negotiated.
func (s *managedStream) emitUsage(ctx context.Context) {
	if s == nil || s.usage == nil || s.emitted {
		return
	}
	s.emitted = true
	evidence, ok := s.totals.event()
	if !ok {
		return
	}
	evidence.Accounting = aipapi.UsageAccountingMetadata{
		Plane:     aipapi.UsagePlaneProviderBillable,
		Source:    aipapi.UsageSourceProviderReported,
		Authority: aipapi.UsageAuthorityAuthoritative,
	}
	s.usage.AddUsageEvent(evidence, s.dedupeKey)
	recordUsageIdentity(ctx, s.profileID, s.identitySource)
}

// recordUsageIdentity records how the completed stream's charge was identified
// for the sideband.
//
// The provider path is ordinary evidence. The attempt fallback is a
// DEGRADATION of correlation - a delivery without a provider identity cannot be
// recognised as a redelivery - so it is recorded at warning level instead of
// living only in a comment. Neither line carries a credential or provider
// content, and the marker never enters the dedupe key: key stability comes
// first.
//
// Identification is not delivery: this runs when the record is handed to the
// sideband buffer, which owns acceptance and drops it when accounting evidence
// was not negotiated. The log therefore claims the charge was identified, not
// that a downstream consumer stored evidence.
func recordUsageIdentity(ctx context.Context, profileID, source string) {
	logger := slog.Default()
	if source == identitySourceAttempt {
		logger.WarnContext(ctx, "openai-chatgpt-plan: usage evidence identified by the host attempt, not a provider response",
			"profile_id", profileID,
			"identity_source", source,
		)
		return
	}
	logger.InfoContext(ctx, "openai-chatgpt-plan: usage evidence identified",
		"profile_id", profileID,
		"identity_source", source,
	)
}

// DrainAccountingEvidence returns the pending V1 sideband records exactly once.
func (s *managedStream) DrainAccountingEvidence() []backendplugin.AccountingEvidence {
	if s == nil || s.usage == nil {
		return nil
	}
	return s.usage.DrainAccountingEvidence()
}

// Cancel aborts the upstream request and releases the connection.
//
// There is no provider-native cancel on this route, so stopping the request is
// a transport-level abort. Content already delivered stays delivered.
func (s *managedStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	if s == nil {
		return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
	}
	if s.cancel != nil {
		s.cancel()
	}
	return aipapi.CancelResult{Mode: aipapi.CancelModeTransport, Err: s.Close()}
}

// Close releases the provider answer.
func (s *managedStream) Close() error {
	if s == nil {
		return nil
	}
	if s.cancel != nil {
		s.cancel()
	}
	if s.inner == nil {
		return nil
	}
	return s.inner.Close()
}

// ReplayAllowed reports whether a transparent replay of this attempt is allowed.
//
// It exists for the no-replay rule the normalizer owns: once client-visible
// output is committed, replay is refused. Nothing in this connector retries,
// so the marker is advisory to core rather than a switch here.
func (s *managedStream) ReplayAllowed() error {
	if s == nil || s.inner == nil {
		return nil
	}
	return s.inner.ReplayAllowed()
}

// The managed stream is the canonical backend stream contract, and its
// accounting sideband is drained through the existing backend-plugin seam.
var (
	_ aipapi.ManagedEventStream              = (*managedStream)(nil)
	_ backendplugin.AccountingEvidenceSource = (*managedStream)(nil)
)
