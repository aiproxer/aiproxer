// Package frontendpipe provides a shared HTTP create pipeline for wire frontends.
package frontendpipe

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/stream"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/decodeqos"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/execerr"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/holdalive"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/jsonguard"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/reqbody"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/sessionwire"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/streamdebug"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
)

// PathMatch carries path-derived inputs for decode (e.g. Gemini model/stream flag).
type PathMatch struct {
	Model  string
	Stream bool
	Extra  any
}

// Decoded is the outcome of protocol decode + canonical validation prep.
type Decoded struct {
	Call   *aipapi.Call
	Stream bool
	// RouteSelector is the resolved routing selector used for decode/logging.
	RouteSelector string
	// RouteOrigin names why the route was chosen. An unset origin leaves the
	// pipeline's own origin in place; a frontend that resolved the route
	// itself, or that restored it from a continuation parent's lineage, reports
	// its own. The distinction matters downstream: only the derived default
	// route may keep the generation's frozen model binding, while an explicit
	// client route always uses the current request-bound registry.
	RouteOrigin routing.RouteOrigin
	// Extra holds per-request protocol state (continuation, response IDs).
	Extra any
}

// HookErrorWriter optionally writes AfterDecode/WrapStream errors with protocol envelopes.
type HookErrorWriter interface {
	WriteHookError(w http.ResponseWriter, err error) error
}

// DecodeErrorWriter optionally preserves protocol-specific decode error fields.
// Frontends without it retain the existing invalid-JSON response.
type DecodeErrorWriter interface {
	WriteDecodeError(w http.ResponseWriter, err error) error
}

// DecodeContext supplies request inputs for protocol decode.
type DecodeContext struct {
	Ctx              context.Context
	Body             []byte
	RouteSelector    string
	Headers          http.Header
	Path             PathMatch
	URLPath          string
	AnthropicVersion string
}

// WireErrors writes protocol-legal JSON (or equivalent) error bodies.
type WireErrors interface {
	WriteBodyTooLarge(w http.ResponseWriter) error
	WriteReadBodyFailed(w http.ResponseWriter) error
	WriteExecutorNotConfigured(w http.ResponseWriter) error
	WritePreflightCanceled(w http.ResponseWriter) error
	WriteInvalidJSON(w http.ResponseWriter) error
	WriteAdmissionReject(w http.ResponseWriter, d decodeqos.Decision) error
	WriteInvalidRequest(w http.ResponseWriter) error
	WriteExecuteError(w http.ResponseWriter, out execerr.Outcome) error
	WriteEncodeFailed(w http.ResponseWriter) error
}

// Config holds shared handler dependencies for the create pipeline.
type Config struct {
	Exec                    aipsdk.ExecutorView
	DefaultRouteSelector    string
	RoutePrefixes           routeselect.PrefixSet
	MaxRequestBodyBytes     int64
	Log                     *slog.Logger
	TrafficPorts            traffic.PortBundle
	DecodeAdmission         aipsdk.DecodeAdmission
	PreRequestKeepalive     aipsdk.FrontendKeepaliveConfig
	FrontendID              string
	HTTPHeaders             aipsdk.HTTPHeaders
	StreamKeepaliveInterval time.Duration
	LargePayload            LargePayloadConfig
}

// Spec parameterizes one frontend's create path.
type Spec[Opts any] struct {
	Config
	Wire WireErrors
	// Profile optionally enables large-payload fast-path candidate evaluation (Task 7.1).
	Profile FrontendProfile
	// OnPreCaptureGate optionally observes candidate gate decisions (Task 7.3).
	OnPreCaptureGate func(r *http.Request, res PreCaptureResult)
	// OnCandidateCapture optionally observes candidate capture outcomes (Task 7.4).
	OnCandidateCapture func(r *http.Request, res CandidateCaptureResult)
	// OnCandidateProof optionally observes candidate protocol proof outcomes (Task 7.5).
	OnCandidateProof func(r *http.Request, res CandidateProofResult)
	// OnCandidateAssessment optionally observes candidate assessment outcomes (Task 11.9).
	OnCandidateAssessment func(r *http.Request, res CandidateAssessmentResult)
	// OnWireCommit optionally observes wire commit execution outcomes (Task 11.9).
	OnWireCommit func(r *http.Request, res WireCommitResult)
	// MatchPath returns ok=false for 404. When AltServe is non-nil and invoked, the pipeline stops.
	MatchPath func(path string) (pm PathMatch, ok bool)
	AltServe  func(ctx context.Context, w http.ResponseWriter, r *http.Request) bool
	// ResolvePreAdmissionRoute runs before body-dependent work; it may use
	// only request headers and path-match facts. It never receives the
	// request body: body-dependent route selection runs under decode
	// admission via ResolveBodyRoute or the shared RouteFromBodyModel path.
	// A nonempty result wins over the route header.
	ResolvePreAdmissionRoute func(headers http.Header, pm PathMatch) routing.ResolvedRoute
	// ResolveBodyRoute runs under the decode-admission permit, next to the
	// shared RouteFromBodyModel extraction. When present its result is
	// authoritative, including an empty selector: it replaces any
	// pre-admission/header route (no header fallback).
	ResolveBodyRoute func(body []byte, pm PathMatch) routing.ResolvedRoute
	Decode           func(DecodeContext) (*Decoded, error)
	AfterDecode      func(ctx context.Context, decoded *Decoded) error
	WrapStream       func(ctx context.Context, decoded *Decoded, inner aipapi.EventStream) (aipapi.EventStream, error)
	OnExecuteError   func(ctx context.Context, decoded *Decoded, err error)
	ClassifyExecute  func(err error) execerr.Outcome
	BuildEncodeOpts  func(decoded *Decoded) Opts
	WriteStream      func(ctx context.Context, w http.ResponseWriter, call *aipapi.Call, es aipapi.EventStream, opts Opts) error
	WriteNonStream   func(ctx context.Context, w http.ResponseWriter, call *aipapi.Call, es aipapi.EventStream, opts Opts) error
	// WireWrapStream optionally wraps the canonical event stream on wire execution using bounded ResponseContext (Requirement 18.4).
	WireWrapStream func(ctx context.Context, rc ResponseContext, inner aipapi.EventStream) (aipapi.EventStream, error)
	// WireWriteStream writes streaming responses on wire execution using bounded ResponseContext (Requirement 18.4).
	WireWriteStream func(ctx context.Context, w http.ResponseWriter, rc ResponseContext, es aipapi.EventStream) error
	// WireWriteNonStream writes non-streaming responses on wire execution using bounded ResponseContext (Requirement 18.4).
	WireWriteNonStream func(ctx context.Context, w http.ResponseWriter, rc ResponseContext, es aipapi.EventStream) error
	// RouteFromBodyModel defaults route selector from JSON model field when header absent.
	RouteFromBodyModel bool
}

func (s *Spec[Opts]) diagnostics() largebody.DiagnosticsObserver {
	if s != nil && s.LargePayload.Diagnostics != nil {
		return s.LargePayload.Diagnostics
	}
	return largebody.NoopDiagnosticsObserver{}
}

func (c Config) maxBodyLimit() int64 {
	if c.MaxRequestBodyBytes > 0 {
		return c.MaxRequestBodyBytes
	}
	return reqbody.DefaultMaxBytes
}

// Logger returns the logger this pipeline reports its internal failures
// through. It never returns nil: a pipeline built without an explicit logger
// falls back to the standard library default logger, so a frontend failure is
// never dropped without a record.
func (c Config) Logger() *slog.Logger {
	return diag.LoggerOrDefault(c.Log)
}

func (c Config) logWriteJSONErr(ctx context.Context, msg string, werr error) {
	if werr == nil {
		return
	}
	diag.LogError(ctx, c.Logger(), msg, diag.AttrOpts{}, werr)
}

func writeHookError[Opts any](ctx context.Context, spec *Spec[Opts], w http.ResponseWriter, err error) {
	if err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		spec.logWriteJSONErr(ctx, "write error json failed", spec.Wire.WritePreflightCanceled(w))
	} else if hw, ok := spec.Wire.(HookErrorWriter); ok {
		spec.logWriteJSONErr(ctx, "write error json failed", hw.WriteHookError(w, err))
	} else {
		spec.logWriteJSONErr(ctx, "write error json failed", spec.Wire.WriteInvalidRequest(w))
	}
}

func rejectInvalidCall[Opts any](ctx context.Context, spec *Spec[Opts], w http.ResponseWriter, call *aipapi.Call) bool {
	if call == nil {
		spec.logWriteJSONErr(ctx, "write error json failed", spec.Wire.WriteInvalidRequest(w))
		return true
	}
	if err := call.Validate(); err != nil {
		diag.LogError(ctx, spec.Logger(), "validate call failed", diag.AttrOpts{CallID: call.ID}, err, slog.String("detail", diag.TruncErrDetail(err, 512)))
		spec.logWriteJSONErr(ctx, "write error json failed", spec.Wire.WriteInvalidRequest(w))
		return true
	}
	return false
}

func classifyExecute[Opts any](spec *Spec[Opts], err error) execerr.Outcome {
	if spec.ClassifyExecute != nil {
		return spec.ClassifyExecute(err)
	}
	return execerr.ClassifyExecute(err)
}

// Fixed reasons for owned-stream disposal diagnostics. The operator-visible
// signal carries one of these plus a bounded category, never raw error or
// panic material.
const (
	streamCleanupFailedReason = "stream_cleanup_failed"
	streamCleanupPanicReason  = "stream_cleanup_panic"
)

// streamCleanupErrorCategory maps a disposal failure to a bounded safe
// category token. It inspects only error identity (cancellation, filesystem
// kinds), never the error string, which may embed a filesystem path or
// request-derived material.
func streamCleanupErrorCategory(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "context_canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "context_deadline"
	}
	if _, ok := errors.AsType[*os.PathError](err); ok {
		return "path_error"
	}
	if _, ok := errors.AsType[*os.LinkError](err); ok {
		return "path_error"
	}
	if _, ok := errors.AsType[*os.SyscallError](err); ok {
		return "filesystem_error"
	}
	return "stream_close_error"
}

// streamCleanupPanicKind maps a recovered disposal panic to a bounded safe
// category token. Error values reuse the error taxonomy above; non-error
// values collapse to one fixed token. The raw recovered value is never
// rendered: it may carry request material.
func streamCleanupPanicKind(recovered any) string {
	if err, ok := recovered.(error); ok && err != nil {
		if kind := streamCleanupErrorCategory(err); kind != "" {
			return kind
		}
	}
	return "disposal_panic"
}

func (c Config) logStreamCleanup(ctx context.Context, msg string, err error) {
	if err == nil {
		return
	}
	logger := c.Logger()
	attrs := append(diag.Attrs(ctx, diag.AttrOpts{}),
		slog.String("reason", streamCleanupFailedReason),
		slog.String("cleanup_error_kind", streamCleanupErrorCategory(err)),
	)
	logger.LogAttrs(ctx, slog.LevelError, msg, attrs...)
}

// logStreamCleanupPanic reports a contained disposal panic through the same
// sanitized path: fixed reason plus bounded kind, never the raw recovered
// value. A contained panic stays visible as diagnostics instead of crashing
// the process and is never silently dropped.
func (c Config) logStreamCleanupPanic(ctx context.Context, msg string, recovered any) {
	logger := c.Logger()
	attrs := append(diag.Attrs(ctx, diag.AttrOpts{}),
		slog.String("reason", streamCleanupPanicReason),
		slog.String("cleanup_error_kind", streamCleanupPanicKind(recovered)),
	)
	logger.LogAttrs(ctx, slog.LevelError, msg, attrs...)
}

// disposeOwnedStream disposes one owned stream with panic containment. A
// panicking Close is reported through the sanitized panic path and never
// propagates, so the caller's original execution error (or abandonment flow)
// always proceeds. A non-panicking Close failure is reported through the
// sanitized failure path; the caller still returns its own original error.
func (c Config) disposeOwnedStream(ctx context.Context, failMsg, panicMsg string, closeFn func() error) {
	reported := func() (closeErr error) {
		defer func() {
			if r := recover(); r != nil {
				c.logStreamCleanupPanic(ctx, panicMsg, r)
			}
		}()
		return closeFn()
	}()
	c.logStreamCleanup(ctx, failMsg, reported)
}

// discardCanonicalStream disposes an abandoned canonical stream without touching
// the response writer. Cleanup failure is reported with fixed safe diagnostics.
func (c Config) discardCanonicalStream(ctx context.Context, es aipapi.EventStream) {
	if es == nil {
		return
	}
	c.disposeOwnedStream(ctx,
		"holdalive abandoned canonical stream close failed",
		"holdalive abandoned canonical stream close panicked",
		es.Close)
}

// closeErrorBearingCanonical disposes a canonical stream that accompanies an
// error the caller cannot consume. Cleanup failure is reported with fixed safe
// diagnostics; the caller always sees the original error.
func (c Config) closeErrorBearingCanonical(ctx context.Context, es aipapi.EventStream) {
	if es == nil {
		return
	}
	c.disposeOwnedStream(ctx,
		"error-bearing canonical stream close failed",
		"error-bearing canonical stream close panicked",
		es.Close)
}

// discardWireStream disposes an abandoned wire result without touching the
// response writer. Cleanup failure is reported with fixed safe diagnostics.
func (c Config) discardWireStream(ctx context.Context, res largebody.ExecutionResult) {
	if res.Stream == nil {
		return
	}
	c.disposeOwnedStream(ctx,
		"holdalive abandoned wire stream close failed",
		"holdalive abandoned wire stream close panicked",
		res.Stream.Close)
}

// closeErrorBearingWire disposes a wire stream that accompanies an error the
// caller cannot consume. Cleanup failure is reported with fixed safe
// diagnostics; the caller always sees the original error.
func (c Config) closeErrorBearingWire(ctx context.Context, res largebody.ExecutionResult) {
	if res.Stream == nil {
		return
	}
	c.disposeOwnedStream(ctx,
		"error-bearing wire stream close failed",
		"error-bearing wire stream close panicked",
		res.Stream.Close)
}

func (c Config) execute(ctx context.Context, w http.ResponseWriter, call *aipapi.Call, stream bool) (aipapi.EventStream, error) {
	if !stream {
		es, err := c.Exec.Execute(ctx, call)
		if err != nil && es != nil {
			c.closeErrorBearingCanonical(ctx, es)
			return nil, err
		}
		return es, err
	}
	hcfg := holdalive.Config{Enabled: c.PreRequestKeepalive.Enabled, Interval: c.PreRequestKeepalive.Interval}
	es, err := holdalive.Wait(ctx, w, hcfg, func(ctx context.Context) (aipapi.EventStream, error) {
		return c.Exec.Execute(ctx, call)
	}, func(abandoned aipapi.EventStream) {
		c.discardCanonicalStream(ctx, abandoned)
	})
	if err != nil && es != nil {
		c.closeErrorBearingCanonical(ctx, es)
		return nil, err
	}
	return es, err
}

func (c Config) executeLargeBody(
	ctx context.Context,
	w http.ResponseWriter,
	wireExec largebody.LargeBodyWireExecutor,
	assessment largebody.Assessment,
	src largebody.Source,
	stream bool,
) (largebody.ExecutionResult, error) {
	if !stream {
		res, err := wireExec.ExecuteLargeBody(ctx, assessment, src)
		if err != nil && res.Stream != nil {
			c.closeErrorBearingWire(ctx, res)
			return largebody.ExecutionResult{}, err
		}
		return res, err
	}
	hcfg := holdalive.Config{Enabled: c.PreRequestKeepalive.Enabled, Interval: c.PreRequestKeepalive.Interval}
	res, err := holdalive.Wait(ctx, w, hcfg, func(ctx context.Context) (largebody.ExecutionResult, error) {
		return wireExec.ExecuteLargeBody(ctx, assessment, src)
	}, func(abandoned largebody.ExecutionResult) {
		c.discardWireStream(ctx, abandoned)
	})
	if err != nil && res.Stream != nil {
		c.closeErrorBearingWire(ctx, res)
		return largebody.ExecutionResult{}, err
	}
	return res, err
}

// ServeHTTP runs the shared decode → execute → encode pipeline.
func ServeHTTP[Opts any](spec *Spec[Opts], w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if spec.StreamKeepaliveInterval > 0 {
		ctx = stream.ContextWithKeepaliveInterval(ctx, spec.StreamKeepaliveInterval)
		r = r.WithContext(ctx)
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if spec.AltServe != nil && spec.AltServe(ctx, w, r) {
		return
	}
	pm, ok := spec.MatchPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	dobs := spec.diagnostics()
	var gateStart time.Time
	if spec.LargePayload.Enabled {
		dobs.OnPipelineStage(largebody.StageConsidered)
		gateStart = time.Now()
	}
	gateRes := EvaluatePreCaptureGates(spec, r)
	if spec.LargePayload.Enabled {
		dobs.OnStageDuration("pre_capture_gates", time.Since(gateStart))
	}
	if spec.OnPreCaptureGate != nil {
		spec.OnPreCaptureGate(r, gateRes)
	}

	limits := jsonguard.Limits{MaxBytes: spec.maxBodyLimit()}
	var body []byte
	var err error
	var capRes CandidateCaptureResult
	if !gateRes.Candidate {
		if spec.LargePayload.Enabled {
			dobs.OnPipelineStage(largebody.StageStaticCanonical)
			declineReason := largebody.ResolveStaticDeclineReason(spec.LargePayload.WireEligibility, gateRes.Reason)
			dobs.OnDecline(declineReason)
		}
		body, err = reqbody.ReadAll(w, r, limits.MaxBytes)
	} else {
		captureStart := time.Now()
		capRes, body, err = CaptureCandidateBody(ctx, spec, w, r, limits.MaxBytes)
		dobs.OnStageDuration("capture", time.Since(captureStart))
		if spec.OnCandidateCapture != nil {
			spec.OnCandidateCapture(r, capRes)
		}
		switch capRes.Outcome {
		case largebody.CaptureOutcomeCompleted:
			dobs.OnPipelineStage(largebody.StageCaptured)
			storage := largebody.StorageMemory
			if capRes.Completed != nil && capRes.Completed.HasSpilled() {
				storage = largebody.StorageFile
			}
			var size int64
			if capRes.Completed != nil {
				size = capRes.Completed.Size()
			} else {
				size = capRes.BytesRead
			}
			dobs.OnCapture(largebody.SizeBucket(size), storage)
			threshold := spec.LargePayload.EffectiveThresholdBytes()
			if capRes.Completed != nil && capRes.Completed.Size() < threshold {
				dobs.OnDecline(largebody.DeclineReasonBelowThreshold)
			}
		case largebody.CaptureOutcomeLimitExceeded:
			dobs.OnDecline(largebody.DeclineReasonLimitExceeded)
		case largebody.CaptureOutcomeReadError:
			dobs.OnDecline(largebody.DeclineReasonReadError)
		case largebody.CaptureOutcomeTerminalError:
			dobs.OnDecline(largebody.DeclineReasonReadError)
		case largebody.CaptureOutcomeDeclined:
			if largebody.IsSpoolBudgetExhausted(capRes.Err) {
				dobs.OnDecline(largebody.DeclineReasonSpoolBudgetExhausted)
			} else {
				dobs.OnDecline(largebody.DeclineReasonReadError)
			}
		}
	}
	if err != nil {
		if capRes.Completed != nil {
			_ = capRes.Completed.Close()
		}
		if reqbody.TooLarge(err) {
			spec.logWriteJSONErr(ctx, "write error json failed", spec.Wire.WriteBodyTooLarge(w))
			return
		}
		if capRes.Outcome == largebody.CaptureOutcomeTerminalError {
			out := execerr.Outcome{
				Status:  http.StatusInternalServerError,
				Kind:    execerr.KindInternalError,
				Message: execerr.InternalWireMessage,
				Err:     err,
			}
			diag.LogError(ctx, spec.Logger(), "terminal capture error", diag.AttrOpts{}, err)
			spec.logWriteJSONErr(ctx, "write error json failed", spec.Wire.WriteExecuteError(w, out))
			return
		}
		spec.logWriteJSONErr(ctx, "write error json failed", spec.Wire.WriteReadBodyFailed(w))
		return
	}
	ct := strings.TrimSpace(r.Header.Get("Content-Type"))
	if ct == "" {
		ct = "application/octet-stream"
	}

	threshold := spec.LargePayload.EffectiveThresholdBytes()
	isCandidateReplay := gateRes.Candidate &&
		capRes.Outcome == largebody.CaptureOutcomeCompleted &&
		capRes.Completed != nil &&
		capRes.Completed.Size() >= threshold

	var decoded *Decoded
	routeOrigin := routing.RouteOriginUnset
	if isCandidateReplay {
		var ok bool
		decoded, body, ok, err = replayCandidate(ctx, spec, w, r, pm, capRes)
		if !ok && err == nil {
			return
		}
	} else {
		if spec.LargePayload.Enabled {
			dobs.OnPipelineStage(largebody.StageCanonical)
		}
		if capRes.Completed != nil {
			_ = capRes.Completed.Close()
		}
		if spec.Exec == nil {
			spec.logWriteJSONErr(ctx, "write error json failed", spec.Wire.WriteExecutorNotConfigured(w))
			return
		}
		resolved := resolvePreAdmissionRoute(spec, r.Header, pm)
		if _, err := jsonguard.PreflightWithContext(ctx, body, limits); err != nil {
			if jsonguard.Classify(err) == jsonguard.KindCanceled {
				spec.logWriteJSONErr(ctx, "write error json failed", spec.Wire.WritePreflightCanceled(w))
				return
			}
			spec.logWriteJSONErr(ctx, "write error json failed", spec.Wire.WriteInvalidJSON(w))
			return
		}
		releaseDecode, ok, aerr := decodeqos.TryAdmit(ctx, spec.DecodeAdmission, int64(len(body)))
		if d := decodeqos.Decide(ok, aerr); d.Status != 0 {
			if d.RetryAfter {
				w.Header().Set("Retry-After", decodeqos.RetryAfterSeconds)
			}
			spec.logWriteJSONErr(ctx, "write error json failed", spec.Wire.WriteAdmissionReject(w, d))
			return
		}
		err = decodeqos.Guard(releaseDecode, func() error {
			// Body-dependent routes resolve under the permit: a full-body
			// JSON parse is the work decode admission bounds, so it must not
			// run before the permit is granted. A custom body hook is
			// authoritative, including an empty selector; otherwise the
			// shared body-model extraction applies only when no route was
			// already named. The two paths are mutually exclusive.
			if spec.ResolveBodyRoute != nil {
				resolved = spec.ResolveBodyRoute(body, pm)
			} else {
				resolved = resolveBodyModelRoute(spec, resolved, body)
			}
			dctx := DecodeContext{
				Ctx:              ctx,
				Body:             body,
				RouteSelector:    resolved.Selector,
				Headers:          r.Header,
				Path:             pm,
				URLPath:          r.URL.Path,
				AnthropicVersion: strings.TrimSpace(r.Header.Get("anthropic-version")),
			}
			var derr error
			decoded, derr = spec.Decode(dctx)
			return derr
		})
		routeOrigin = resolved.Origin
	}
	if err != nil {
		log := spec.Logger()
		diag.LogError(ctx, log, "decode request failed", diag.AttrOpts{}, err, slog.String("detail", diag.TruncErrDetail(err, 512)))
		streamdebug.LogDecodeFailure(ctx, log, spec.FrontendID, body, err)
		if writer, ok := spec.Wire.(DecodeErrorWriter); ok {
			spec.logWriteJSONErr(ctx, "write error json failed", writer.WriteDecodeError(w, err))
		} else {
			spec.logWriteJSONErr(ctx, "write error json failed", spec.Wire.WriteInvalidJSON(w))
		}
		return
	}
	if decoded != nil && decoded.Call != nil {
		hdrs := spec.HTTPHeaders.OrDefault()
		sessionwire.ApplyAuthoritativeHeadersNamed(&decoded.Call.Session, r.Header, hdrs.SessionID, hdrs.ResumeToken)
	}
	call := decoded.Call
	if rejectInvalidCall(ctx, spec, w, call) {
		return
	}
	if spec.AfterDecode != nil {
		if err := spec.AfterDecode(ctx, decoded); err != nil {
			writeHookError(ctx, spec, w, err)
			return
		}
		call = decoded.Call
		if rejectInvalidCall(ctx, spec, w, call) {
			return
		}
	}
	// The route origin is the route's provenance, and it is final here: the
	// decode and the post-decode hook had every chance to resolve the route
	// themselves, and a frontend that restored it from a continuation parent's
	// lineage reports that origin.
	origin := routeOrigin
	if decoded != nil && decoded.RouteOrigin != routing.RouteOriginUnset {
		origin = decoded.RouteOrigin
	}
	ctx = routing.WithRouteOrigin(ctx, origin)

	traceID := diag.StableCallID(call)
	ctx = diag.EnsureCallDiag(ctx, traceID, strings.TrimSpace(call.Session.ALegID))
	spec.TrafficPorts.Emit(ctx, traffic.LegCTP, traffic.CaptureMeta{
		TraceID:   traceID,
		SessionID: call.Session.CorrelationID(),
	}, "http", ct, body)

	stream := decoded.Stream
	if pm.Model != "" {
		stream = pm.Stream
		decoded.Stream = stream
	}

	streamdebug.LogCall(ctx, spec.Log, spec.FrontendID, call, stream, len(body), decoded.RouteSelector)
	executeStart := time.Now()
	es, err := spec.execute(ctx, w, call, stream)
	if err != nil {
		if spec.OnExecuteError != nil {
			spec.OnExecuteError(ctx, decoded, err)
		}
		out := classifyExecute(spec, err)
		if execerr.ShouldLogExecuteError(out) {
			diag.LogError(ctx, spec.Logger(), "execute failed", diag.AttrOpts{CallID: call.ID}, out.Err)
		}
		spec.logWriteJSONErr(ctx, "write error json failed", spec.Wire.WriteExecuteError(w, out))
		return
	}
	if spec.WrapStream != nil {
		inner := es
		es, err = spec.WrapStream(ctx, decoded, es)
		if err != nil {
			if inner != nil {
				_ = inner.Close()
			}
			if spec.OnExecuteError != nil {
				spec.OnExecuteError(ctx, decoded, err)
			}
			out := classifyExecute(spec, err)
			spec.logWriteJSONErr(ctx, "write error json failed", spec.Wire.WriteExecuteError(w, out))
			return
		}
	}
	streamdebug.LogExecuteOpened(ctx, spec.Log, spec.FrontendID, call, executeStart)

	ctx = diag.EnsureCallDiag(ctx, traceID, call.Session.ALegID)
	es = streamdebug.Wrap(ctx, spec.Log, spec.FrontendID, call, es, executeStart)

	opts := spec.BuildEncodeOpts(decoded)
	if stream {
		if err := spec.WriteStream(ctx, w, call, es, opts); err != nil {
			diag.LogError(ctx, spec.Logger(), "stream encode failed", diag.AttrOpts{CallID: call.ID}, err)
			return
		}
		return
	}
	if err := spec.WriteNonStream(ctx, w, call, es, opts); err != nil {
		// Non-streaming encoders Collect the canonical stream before writing
		// headers, so a capability rejection surfacing through Recv arrives
		// here rather than through Execute. Classify it like an execution
		// error: client rejects keep their 4xx wire shape and backend-
		// unavailable rejects keep their 502 wire shape (frontend-owned
		// ClassifyExecute still applies), while genuine encode/infrastructure
		// failures retain the static 500.
		if out := classifyExecute(spec, err); out.Kind == execerr.KindClientReject || out.Kind == execerr.KindBackendUnavailable {
			spec.logWriteJSONErr(ctx, "write error json failed", spec.Wire.WriteExecuteError(w, out))
		} else {
			diag.LogError(ctx, spec.Logger(), "non-stream encode failed", diag.AttrOpts{CallID: call.ID}, err)
			spec.logWriteJSONErr(ctx, "write error json failed", spec.Wire.WriteEncodeFailed(w))
		}
	}
}
