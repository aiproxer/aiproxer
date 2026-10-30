package metering

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipsdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

const (
	// BoundaryMappingRef identifies the provider-neutral local boundary mapping.
	BoundaryMappingRef = "aip.local.boundary.v1"
	// DefaultBoundaryMaxTextBytes limits text accounting without retaining text.
	DefaultBoundaryMaxTextBytes = 64 * 1024
	// DefaultBoundaryMaxMediaEntries limits media property entries per plane.
	DefaultBoundaryMaxMediaEntries = 128
	maxBoundaryMethodRefBytes      = 512
	maxBoundaryMIMEBytes           = 256
	maxBoundaryValue               = int64(1 << 60)
)

// MediaKind identifies a provider-neutral media shape at a boundary.
type MediaKind string

const (
	MediaImage    MediaKind = "image"
	MediaAudio    MediaKind = "audio"
	MediaVideo    MediaKind = "video"
	MediaDocument MediaKind = "document"
	MediaFile     MediaKind = "file"
)

func (k MediaKind) valid() bool {
	switch k {
	case MediaImage, MediaAudio, MediaVideo, MediaDocument, MediaFile:
		return true
	default:
		return false
	}
}

// MediaSummary is a bounded media property summary. It never contains raw
// media; presence flags distinguish an observed zero from an unknown value.
type MediaSummary struct {
	Kind            MediaKind
	Count           int64
	Bytes           int64
	BytesPresent    bool
	DurationMillis  int64
	DurationPresent bool
	Frames          int64
	FramesPresent   bool
	Pages           int64
	PagesPresent    bool
	WidthPixels     int64
	WidthPresent    bool
	HeightPixels    int64
	HeightPresent   bool
	MIME            string
}

// PreparedInputSummary describes the final provider-bound representation.
// Adapters report it after payload rewrites/transforms through the observer.
type PreparedInputSummary struct {
	TextBytes           int64
	TextBytesPresent    bool
	PayloadBytes        int64
	PayloadBytesPresent bool
	TextTokens          int64
	TextTokensPresent   bool
	TextTokensEstimated bool
	Media               []MediaSummary
	TextTruncated       bool
	MediaTruncated      bool
	Truncated           bool
	MethodRef           string
}

// BoundaryConfig bounds local observation capture. Zero values select defaults.
type BoundaryConfig struct {
	MaxTextBytes    int
	MaxMediaEntries int
}

func (c BoundaryConfig) normalized() BoundaryConfig {
	if c.MaxTextBytes <= 0 || c.MaxTextBytes > DefaultBoundaryMaxTextBytes {
		c.MaxTextBytes = DefaultBoundaryMaxTextBytes
	}
	if c.MaxMediaEntries <= 0 || c.MaxMediaEntries > DefaultBoundaryMaxMediaEntries {
		c.MaxMediaEntries = DefaultBoundaryMaxMediaEntries
	}
	return c
}

// PreparedInputSnapshot records the final input and its backend-open lifecycle.
type PreparedInputSnapshot struct {
	Prepared            bool
	Attempted           bool
	Accepted            bool
	AcceptedKnown       bool
	AdapterProvided     bool
	TextBytes           int64
	TextBytesPresent    bool
	PayloadBytes        int64
	PayloadBytesPresent bool
	TextTokens          int64
	TextTokensPresent   bool
	TextTokensEstimated bool
	TextTruncated       bool
	Media               []MediaSummary
	MediaTruncated      bool
	Truncated           bool
	MethodRef           string
}

// OutputSnapshot contains bounded provider-origin or customer-egress data.
type OutputSnapshot struct {
	TextBytes           int64
	TextBytesPresent    bool
	TextTokens          int64
	TextTokensPresent   bool
	TextTokensEstimated bool
	TextTruncated       bool
	Media               []MediaSummary
	MediaTruncated      bool
	Truncated           bool
}

// BoundarySnapshot is an immutable copy of all local boundary planes.
type BoundarySnapshot struct {
	Input          PreparedInputSnapshot
	ProviderOutput OutputSnapshot
	CustomerOutput OutputSnapshot
}

// ProviderBinding carries the trusted host facts required to convert an
// opt-in connector-supplied provider claim into a host-bound observation. It is
// consumed only by the executable-connector V2 bind path; the in-process
// provider evidence buffer ignores it, so binding a connector claim can never
// change the ordinary provider path.
type ProviderBinding struct {
	// AccountID is the credited billing account frozen for this call.
	AccountID string
	// Scope is the customer scope frozen at exposure admission.
	Scope scope.PrincipalScopeView
	// ScopePresent reports whether a trusted scope was actually frozen for
	// this call. It is read from the carrier, never inferred from the view:
	// a present scope whose fields are all unknown is a different fact from
	// an absent scope, and the durable identity must tell them apart.
	ScopePresent bool
}

// ObservationIdentity is trusted request/B-leg lineage supplied by runtime.
// Missing StoreID or BLegID suppresses observations instead of inventing scope.
type ObservationIdentity struct {
	StoreID       string
	RequestID     string
	CallID        string
	BillingCallID string
	ALegID        string
	BLegID        string
	AttemptID     string
	AttemptSeq    uint64
	Scope         scope.PrincipalScopeView
	ObservedAt    time.Time
	ReceivedAt    time.Time
	// Provider carries the trusted host facts of the opt-in connector bind
	// path. The ordinary in-process provider evidence buffer never reads it.
	Provider ProviderBinding
}

// BoundaryAccumulator owns one attempt's bounded local boundary state.
type BoundaryAccumulator struct {
	mu             sync.Mutex
	config         BoundaryConfig
	input          PreparedInputSnapshot
	providerOutput OutputSnapshot
	customerOutput OutputSnapshot
}

// NewBoundaryAccumulator creates bounded in-memory observation state.
func NewBoundaryAccumulator(config ...BoundaryConfig) *BoundaryAccumulator {
	var selected BoundaryConfig
	if len(config) > 0 {
		selected = config[0]
	}
	return &BoundaryAccumulator{config: selected.normalized()}
}

// PrepareCall records a bounded canonical fallback before adapter opening.
func (a *BoundaryAccumulator) PrepareCall(call aipapi.Call) {
	if a == nil {
		return
	}
	summary := preparedInputSummaryFromCall(call, a.config)
	a.mu.Lock()
	a.input = snapshotFromPreparedSummary(summary, false)
	a.input.Prepared = true
	a.mu.Unlock()
}

// PreparePayloadBytes records a wire-only payload-byte fallback. A negative
// length remains absent rather than being converted to a false observed zero.
func (a *BoundaryAccumulator) PreparePayloadBytes(bytes int64) {
	if a == nil {
		return
	}
	summary := normalizePreparedSummary(PreparedInputSummary{
		PayloadBytes: bytes, PayloadBytesPresent: bytes >= 0,
		MethodRef: "local.boundary.wire_payload.v1",
	}, a.config)
	a.mu.Lock()
	priorAttempted, priorAccepted, priorAcceptedKnown := a.input.Attempted, a.input.Accepted, a.input.AcceptedKnown
	a.input = snapshotFromPreparedSummary(summary, false)
	a.input.Prepared = true
	a.input.Attempted = priorAttempted
	a.input.Accepted = priorAccepted
	a.input.AcceptedKnown = priorAcceptedKnown
	a.mu.Unlock()
}

// ObservePreparedInput replaces the canonical fallback with an adapter's final
// provider-bound summary. It never imports provider count evidence.
func (a *BoundaryAccumulator) ObservePreparedInput(summary PreparedInputSummary) {
	if a == nil {
		return
	}
	summary = normalizePreparedSummary(summary, a.config)
	if summary.TextBytesPresent && !summary.TextTokensPresent {
		summary.TextTokens = estimateTokens(summary.TextBytes)
		summary.TextTokensPresent = true
		summary.TextTokensEstimated = true
	}
	a.mu.Lock()
	priorAttempted, priorAccepted, priorAcceptedKnown := a.input.Attempted, a.input.Accepted, a.input.AcceptedKnown
	a.input = snapshotFromPreparedSummary(summary, true)
	a.input.Prepared = true
	a.input.Attempted = priorAttempted
	a.input.Accepted = priorAccepted
	a.input.AcceptedKnown = priorAcceptedKnown
	a.mu.Unlock()
}

// MarkAttempted records that backend opening crossed the local attempt boundary.
func (a *BoundaryAccumulator) MarkAttempted() {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.input.Prepared = true
	a.input.Attempted = true
	a.mu.Unlock()
}

// MarkAccepted records an observed backend-open result.
func (a *BoundaryAccumulator) MarkAccepted(accepted bool) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.input.Prepared = true
	a.input.Attempted = true
	a.input.AcceptedKnown = true
	a.input.Accepted = accepted
	a.mu.Unlock()
}

// ObserveProviderEvent captures provider-origin content before projection.
// Adapters may provide bounded post-decode media properties explicitly.
func (a *BoundaryAccumulator) ObserveProviderEvent(event aipapi.Event, media ...MediaSummary) {
	if a == nil {
		return
	}
	a.mu.Lock()
	observeOutputEvent(&a.providerOutput, event, media, a.config)
	a.mu.Unlock()
}

// ObserveCustomerEvent captures the post-projection customer-egress plane.
func (a *BoundaryAccumulator) ObserveCustomerEvent(event aipapi.Event, media ...MediaSummary) {
	if a == nil {
		return
	}
	a.mu.Lock()
	observeOutputEvent(&a.customerOutput, event, media, a.config)
	a.mu.Unlock()
}

// Snapshot returns a deep copy of mutable media slices.
func (a *BoundaryAccumulator) Snapshot() BoundarySnapshot {
	if a == nil {
		return BoundarySnapshot{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	out := BoundarySnapshot{Input: a.input, ProviderOutput: a.providerOutput, CustomerOutput: a.customerOutput}
	out.Input.Media = cloneMedia(a.input.Media)
	out.ProviderOutput.Media = cloneMedia(a.providerOutput.Media)
	out.CustomerOutput.Media = cloneMedia(a.customerOutput.Media)
	return out
}

// Observations snapshots and converts local state into V2 envelopes without I/O.
func (a *BoundaryAccumulator) Observations(identity ObservationIdentity) []aipsdkmetering.Observation {
	if a == nil {
		return nil
	}
	return a.Snapshot().Observations(identity)
}

// PreparedInputObserver is the adapter-to-core final-boundary callback.
type PreparedInputObserver func(PreparedInputSummary)

type preparedInputObserverKey struct{}

// PreparedInputSummaryBuilder is a bounded adapter-side builder for the final
// provider representation. It accepts properties without retaining payload
// bytes; adapters can therefore inspect SDK params directly without marshaling
// or cloning the request body solely for accounting.
type PreparedInputSummaryBuilder struct {
	summary PreparedInputSummary
	config  BoundaryConfig
}

// NewPreparedInputSummaryBuilder starts a bounded summary with the adapter's
// method reference. The returned value is intentionally stack-friendly.
func NewPreparedInputSummaryBuilder(method string) PreparedInputSummaryBuilder {
	return PreparedInputSummaryBuilder{summary: PreparedInputSummary{MethodRef: method}, config: BoundaryConfig{}.normalized()}
}

// AddText records text bytes from the provider-bound representation.
func (b *PreparedInputSummaryBuilder) AddText(text string) {
	if b == nil {
		return
	}
	b.AddTextBytes(int64(len(text)))
}

// AddTextBytes records a bounded text-byte property without retaining text.
func (b *PreparedInputSummaryBuilder) AddTextBytes(bytes int64) {
	if b == nil {
		return
	}
	addBoundedText(&b.summary.TextBytes, &b.summary.TextBytesPresent, &b.summary.Truncated, bytes, int64(b.config.normalized().MaxTextBytes))
}

// AddMedia records one bounded provider-bound media property set.
func (b *PreparedInputSummaryBuilder) AddMedia(media MediaSummary) {
	if b == nil {
		return
	}
	config := b.config.normalized()
	b.summary.Media = appendMedia(b.summary.Media, media, config, &b.summary.Truncated, &b.summary.MediaTruncated)
}

// SetPayloadBytes records a bounded payload metadata value when the adapter
// already knows it. It never requires constructing a payload copy.
func (b *PreparedInputSummaryBuilder) SetPayloadBytes(bytes int64) {
	if b == nil {
		return
	}
	if bytes < 0 {
		return
	}
	max := int64(b.config.normalized().MaxTextBytes)
	addBoundedText(&b.summary.PayloadBytes, &b.summary.PayloadBytesPresent, &b.summary.Truncated, bytes, max)
}

// Build returns a bounded summary with an ownership-safe media slice.
func (b *PreparedInputSummaryBuilder) Build() PreparedInputSummary {
	if b == nil {
		return PreparedInputSummary{}
	}
	out := b.summary
	out.Media = cloneMedia(b.summary.Media)
	return out
}

// WithPreparedInputObserver attaches a request-scoped prepared-input callback.
func WithPreparedInputObserver(ctx context.Context, observer PreparedInputObserver) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if observer == nil {
		return ctx
	}
	return context.WithValue(ctx, preparedInputObserverKey{}, observer)
}

// PreparedInputObservationEnabled reports whether a final-boundary observer is
// attached. Adapter summary builders use it to keep disabled accounting free of
// payload-sized traversal or temporary summary allocations.
func PreparedInputObservationEnabled(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	observer, ok := ctx.Value(preparedInputObserverKey{}).(PreparedInputObserver)
	return ok && observer != nil
}

// ObservePreparedInput invokes an attached callback, if any.
func ObservePreparedInput(ctx context.Context, summary PreparedInputSummary) {
	if ctx == nil {
		return
	}
	observer, ok := ctx.Value(preparedInputObserverKey{}).(PreparedInputObserver)
	if !ok || observer == nil {
		return
	}
	observer(summary)
}

// ObservePreparedInputCall computes the bounded canonical estimate only when
// a request-scoped observer is attached. This keeps ordinary accounting-off
// adapters free of media-summary allocations while retaining the same neutral
// callback seam for enabled capture.
func ObservePreparedInputCall(ctx context.Context, call aipapi.Call, method string) {
	if ctx == nil {
		return
	}
	observer, ok := ctx.Value(preparedInputObserverKey{}).(PreparedInputObserver)
	if !ok || observer == nil {
		return
	}
	summary := PreparedInputSummaryFromCall(call)
	summary.MethodRef = method
	observer(summary)
}

func snapshotFromPreparedSummary(summary PreparedInputSummary, adapterProvided bool) PreparedInputSnapshot {
	return PreparedInputSnapshot{
		AdapterProvided: adapterProvided, TextBytes: summary.TextBytes, TextBytesPresent: summary.TextBytesPresent,
		PayloadBytes: summary.PayloadBytes, PayloadBytesPresent: summary.PayloadBytesPresent,
		TextTokens: summary.TextTokens, TextTokensPresent: summary.TextTokensPresent,
		TextTokensEstimated: summary.TextTokensEstimated, TextTruncated: summary.TextTruncated,
		Media: cloneMedia(summary.Media), MediaTruncated: summary.MediaTruncated, Truncated: summary.Truncated,
		MethodRef: summary.MethodRef,
	}
}

func normalizePreparedSummary(summary PreparedInputSummary, config BoundaryConfig) PreparedInputSummary {
	config = config.normalized()
	var changed bool
	summary.TextBytes, summary.TextBytesPresent, changed = boundedValue(summary.TextBytes, summary.TextBytesPresent, int64(config.MaxTextBytes))
	if changed {
		summary.TextTruncated, summary.Truncated = true, true
	}
	summary.PayloadBytes, summary.PayloadBytesPresent, changed = boundedValue(summary.PayloadBytes, summary.PayloadBytesPresent, int64(config.MaxTextBytes))
	if changed {
		summary.Truncated = true
	}
	if summary.TextTokensEstimated && !summary.TextTokensPresent {
		summary.TextTokensPresent = true
	}
	if summary.TextTokensPresent {
		if summary.TextTokens < 0 {
			summary.TextTokens, summary.Truncated = 0, true
		}
		if summary.TextTokens > maxBoundaryValue {
			summary.TextTokens, summary.Truncated = maxBoundaryValue, true
		}
	}
	var mediaChanged bool
	summary.Media, summary.Truncated, mediaChanged = normalizeMedia(summary.Media, config.MaxMediaEntries, summary.Truncated)
	summary.MediaTruncated = summary.MediaTruncated || mediaChanged
	summary.MethodRef = boundedMethodRef(summary.MethodRef)
	return summary
}

func preparedInputSummaryFromCall(call aipapi.Call, config BoundaryConfig) PreparedInputSummary {
	config = config.normalized()
	summary := PreparedInputSummary{TextBytesPresent: true}
	addText := func(n int) {
		addBoundedText(&summary.TextBytes, &summary.TextBytesPresent, &summary.TextTruncated, int64(n), int64(config.MaxTextBytes))
		if summary.TextTruncated {
			summary.Truncated = true
		}
	}
	addContentPart := func(part aipapi.ContentPart) {
		switch part.Kind {
		case aipapi.ContentPartText:
			addText(len(part.Text))
		case aipapi.ContentPartJSON, aipapi.ContentPartToolResult, aipapi.ContentPartRefusal, aipapi.ContentPartSummary:
			addText(len(part.Text))
		case aipapi.ContentPartReasoning:
			if part.Reasoning != nil {
				addText(len(part.Reasoning.Text))
			}
		case aipapi.ContentPartImageRef:
			summary.Media = appendMedia(summary.Media, MediaSummary{Kind: MediaImage, Count: 1, MIME: part.ImageMIME}, config, &summary.Truncated, &summary.MediaTruncated)
		case aipapi.ContentPartVideoRef:
			summary.Media = appendMedia(summary.Media, MediaSummary{Kind: MediaVideo, Count: 1, MIME: part.VideoMIME}, config, &summary.Truncated, &summary.MediaTruncated)
		case aipapi.ContentPartFileRef:
			kind := mediaKindFromMIME(part.FileMIME, MediaFile)
			summary.Media = appendMedia(summary.Media, MediaSummary{Kind: kind, Count: 1, Bytes: int64(len(part.FileData)), BytesPresent: part.FileData != "", MIME: part.FileMIME}, config, &summary.Truncated, &summary.MediaTruncated)
		}
	}
	addPart := func(part aipapi.Part) {
		switch part.Kind {
		case aipapi.PartText, aipapi.PartToolResult:
			addText(len(part.Text))
		case aipapi.PartJSON:
			addText(len(part.Content))
		case aipapi.PartReasoning:
			if part.Reasoning != nil {
				addText(len(part.Reasoning.Text))
			}
		case aipapi.PartImageRef:
			summary.Media = appendMedia(summary.Media, MediaSummary{Kind: MediaImage, Count: 1, MIME: part.ImageMIME}, config, &summary.Truncated, &summary.MediaTruncated)
		case aipapi.PartFileRef:
			summary.Media = appendMedia(summary.Media, MediaSummary{Kind: mediaKindFromMIME(part.FileMIME, MediaFile), Count: 1, MIME: part.FileMIME}, config, &summary.Truncated, &summary.MediaTruncated)
		default:
			if part.Text != "" {
				addText(len(part.Text))
			}
		}
	}
	if call.HasItemAuthority() {
		for _, item := range call.Items {
			for _, part := range item.Content {
				addContentPart(part)
			}
			if item.ToolCall != nil {
				addText(len(item.ToolCall.Arguments))
			}
			if item.ToolResult != nil {
				addText(len(item.ToolResult.Output))
				for _, part := range item.ToolResult.Parts {
					addContentPart(part)
				}
			}
			if item.Reasoning != nil && item.Reasoning.Reasoning != nil {
				addText(len(item.Reasoning.Reasoning.Text))
			}
		}
	} else {
		for _, message := range call.Instructions {
			for _, part := range message.Parts {
				addPart(part)
			}
		}
		for _, message := range call.Messages {
			for _, part := range message.Parts {
				addPart(part)
			}
		}
	}
	for _, tool := range call.Tools {
		addText(len(tool.Name))
		addText(len(tool.Description))
		addText(len(tool.Parameters))
	}
	summary = normalizePreparedSummary(summary, config)
	if summary.TextBytesPresent && !summary.TextTokensPresent {
		summary.TextTokens = estimateTokens(summary.TextBytes)
		summary.TextTokensPresent = true
		summary.TextTokensEstimated = true
	}
	summary.MethodRef = "local.boundary.canonical.v1"
	return summary
}

// PreparedInputSummaryFromCall returns the bounded canonical fallback summary.
func PreparedInputSummaryFromCall(call aipapi.Call) PreparedInputSummary {
	return preparedInputSummaryFromCall(call, BoundaryConfig{})
}

// SummaryFromCall is a concise compatibility alias for adapter integrations.
func SummaryFromCall(call aipapi.Call) PreparedInputSummary {
	return PreparedInputSummaryFromCall(call)
}

func observeOutputEvent(out *OutputSnapshot, event aipapi.Event, explicit []MediaSummary, config BoundaryConfig) {
	if out == nil {
		return
	}
	config = config.normalized()
	if event.Kind == aipapi.EventTextDelta {
		addBoundedText(&out.TextBytes, &out.TextBytesPresent, &out.TextTruncated, int64(len(event.Delta)), int64(config.MaxTextBytes))
		if out.TextTruncated {
			out.Truncated = true
		}
		if out.TextBytesPresent {
			out.TextTokens = estimateTokens(out.TextBytes)
			out.TextTokensPresent = true
			out.TextTokensEstimated = true
		}
	}

	if len(explicit) != 0 {
		for _, item := range explicit {
			out.Media = appendMedia(out.Media, item, config, &out.Truncated, &out.MediaTruncated)
		}
		return
	}

	switch event.Kind {
	case aipapi.EventAssistantImageRef:
		out.Media = appendMedia(out.Media, MediaSummary{Kind: MediaImage, Count: 1, MIME: event.AssistantMIME}, config, &out.Truncated, &out.MediaTruncated)
	case aipapi.EventAssistantFileRef:
		out.Media = appendMedia(out.Media, MediaSummary{Kind: mediaKindFromMIME(event.AssistantMIME, MediaFile), Count: 1, MIME: event.AssistantMIME}, config, &out.Truncated, &out.MediaTruncated)
	case aipapi.EventItem:
		if event.Item != nil {
			for _, part := range event.Item.Content {
				switch part.Kind {
				case aipapi.ContentPartText:
					addBoundedText(&out.TextBytes, &out.TextBytesPresent, &out.TextTruncated, int64(len(part.Text)), int64(config.MaxTextBytes))
				case aipapi.ContentPartRefusal:
					addBoundedText(&out.TextBytes, &out.TextBytesPresent, &out.TextTruncated, int64(len(part.Refusal)), int64(config.MaxTextBytes))
				case aipapi.ContentPartSummary:
					addBoundedText(&out.TextBytes, &out.TextBytesPresent, &out.TextTruncated, int64(len(part.Summary)), int64(config.MaxTextBytes))
				case aipapi.ContentPartImageRef:
					out.Media = appendMedia(out.Media, MediaSummary{Kind: MediaImage, Count: 1, MIME: part.ImageMIME}, config, &out.Truncated, &out.MediaTruncated)
				case aipapi.ContentPartVideoRef:
					out.Media = appendMedia(out.Media, MediaSummary{Kind: MediaVideo, Count: 1, MIME: part.VideoMIME}, config, &out.Truncated, &out.MediaTruncated)
				case aipapi.ContentPartFileRef:
					out.Media = appendMedia(out.Media, MediaSummary{Kind: mediaKindFromMIME(part.FileMIME, MediaFile), Count: 1, Bytes: int64(len(part.FileData)), BytesPresent: part.FileData != "", MIME: part.FileMIME}, config, &out.Truncated, &out.MediaTruncated)
				}
			}
			if out.TextTruncated {
				out.Truncated = true
			}
			if out.TextBytesPresent {
				out.TextTokens = estimateTokens(out.TextBytes)
				out.TextTokensPresent = true
				out.TextTokensEstimated = true
			}
		}
	}
}

func cloneMedia(media []MediaSummary) []MediaSummary {
	if media == nil {
		return nil
	}
	out := make([]MediaSummary, len(media))
	copy(out, media)
	return out
}

func estimateTokens(bytes int64) int64 {
	if bytes <= 0 {
		return 0
	}
	// This is deliberately a method-labelled estimate, not a provider token
	// count. Four bytes per token is a bounded, deterministic approximation.
	if bytes > maxBoundaryValue-3 {
		return maxBoundaryValue
	}
	return (bytes + 3) / 4
}

func boundedValue(value int64, present bool, max int64) (int64, bool, bool) {
	if !present {
		return 0, false, false
	}
	if max <= 0 || max > maxBoundaryValue {
		max = maxBoundaryValue
	}
	if value < 0 {
		return 0, false, true
	}
	if value > max {
		return max, true, true
	}
	return value, true, false
}

func addBoundedText(current *int64, present *bool, truncated *bool, add, max int64) {
	if current == nil || present == nil || truncated == nil || add < 0 {
		return
	}
	if max <= 0 || max > maxBoundaryValue {
		max = maxBoundaryValue
	}
	*present = true
	if *current >= max {
		*current = max
		if add != 0 {
			*truncated = true
		}
		return
	}
	if add > max-*current {
		*current = max
		*truncated = true
		return
	}
	*current += add
}

func appendMedia(existing []MediaSummary, media MediaSummary, config BoundaryConfig, truncated, mediaTruncated *bool) []MediaSummary {
	config = config.normalized()
	if len(existing) >= config.MaxMediaEntries {
		if truncated != nil {
			*truncated = true
		}
		if mediaTruncated != nil {
			*mediaTruncated = true
		}
		return existing
	}
	normalized, changed, accepted := normalizeMediaEntry(media)
	if !accepted {
		if truncated != nil {
			*truncated = true
		}
		if mediaTruncated != nil {
			*mediaTruncated = true
		}
		return existing
	}
	if changed {
		if truncated != nil {
			*truncated = true
		}
		if mediaTruncated != nil {
			*mediaTruncated = true
		}
	}
	return append(existing, normalized)
}

func normalizeMedia(media []MediaSummary, maxEntries int, truncated bool) ([]MediaSummary, bool, bool) {
	if maxEntries <= 0 || maxEntries > DefaultBoundaryMaxMediaEntries {
		maxEntries = DefaultBoundaryMaxMediaEntries
	}
	if media == nil {
		return nil, truncated, false
	}
	out := make([]MediaSummary, 0, minInt(len(media), maxEntries))
	changed := false
	for _, item := range media {
		if len(out) >= maxEntries {
			changed = true
			truncated = true
			break
		}
		normalized, itemChanged, accepted := normalizeMediaEntry(item)
		if !accepted {
			changed = true
			truncated = true
			continue
		}
		if itemChanged {
			changed = true
			truncated = true
		}
		out = append(out, normalized)
	}
	return out, truncated, changed
}

func normalizeMediaEntry(media MediaSummary) (MediaSummary, bool, bool) {
	if !media.Kind.valid() {
		return MediaSummary{}, true, false
	}
	changed := false
	for name, value := range map[string]int64{
		"count": media.Count, "bytes": media.Bytes, "duration": media.DurationMillis,
		"frames": media.Frames, "pages": media.Pages, "width": media.WidthPixels, "height": media.HeightPixels,
	} {
		if value < 0 {
			changed = true
			switch name {
			case "count":
				media.Count = 0
			case "bytes":
				media.Bytes, media.BytesPresent = 0, false
			case "duration":
				media.DurationMillis, media.DurationPresent = 0, false
			case "frames":
				media.Frames, media.FramesPresent = 0, false
			case "pages":
				media.Pages, media.PagesPresent = 0, false
			case "width":
				media.WidthPixels, media.WidthPresent = 0, false
			case "height":
				media.HeightPixels, media.HeightPresent = 0, false
			}
		} else if value > maxBoundaryValue {
			changed = true
			switch name {
			case "count":
				media.Count = maxBoundaryValue
			case "bytes":
				media.Bytes = maxBoundaryValue
			case "duration":
				media.DurationMillis = maxBoundaryValue
			case "frames":
				media.Frames = maxBoundaryValue
			case "pages":
				media.Pages = maxBoundaryValue
			case "width":
				media.WidthPixels = maxBoundaryValue
			case "height":
				media.HeightPixels = maxBoundaryValue
			}
		}
	}
	bounded := boundedMIME(media.MIME)
	if bounded != media.MIME {
		changed = true
		media.MIME = bounded
	}
	return media, changed, true
}

func boundedMIME(value string) string {
	return boundedSafeString(value, maxBoundaryMIMEBytes)
}

func boundedMethodRef(value string) string {
	return boundedSafeString(value, maxBoundaryMethodRefBytes)
}

func boundedSafeString(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) > maxBytes {
		value = value[:maxBytes]
		for len(value) > 0 && !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f || !unicode.IsPrint(r) {
			return ""
		}
	}
	return value
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func mediaKindFromMIME(mime string, fallback MediaKind) MediaKind {
	lower := strings.ToLower(strings.TrimSpace(mime))
	switch {
	case strings.HasPrefix(lower, "image/"):
		return MediaImage
	case strings.HasPrefix(lower, "audio/"):
		return MediaAudio
	case strings.HasPrefix(lower, "video/"):
		return MediaVideo
	case lower == "application/pdf", strings.HasPrefix(lower, "application/msword"), strings.HasPrefix(lower, "application/vnd.openxmlformats-officedocument"), strings.HasPrefix(lower, "text/"):
		return MediaDocument
	default:
		return fallback
	}
}

// Observations converts the three local boundary planes into durable V2
// envelopes. The conversion is deliberately pure: it does not call a
// recorder, journal, provider, or rating implementation.
func (s BoundarySnapshot) Observations(identity ObservationIdentity) []aipsdkmetering.Observation {
	identity.StoreID = strings.TrimSpace(identity.StoreID)
	identity.RequestID = strings.TrimSpace(identity.RequestID)
	identity.CallID = strings.TrimSpace(identity.CallID)
	identity.BillingCallID = strings.TrimSpace(identity.BillingCallID)
	identity.ALegID = strings.TrimSpace(identity.ALegID)
	identity.BLegID = strings.TrimSpace(identity.BLegID)
	identity.AttemptID = strings.TrimSpace(identity.AttemptID)
	if identity.StoreID == "" || identity.BLegID == "" {
		return nil
	}
	if identity.BillingCallID == "" {
		identity.BillingCallID = identity.CallID
	}
	if identity.CallID == "" {
		identity.CallID = identity.BillingCallID
	}
	if identity.AttemptID == "" {
		identity.AttemptID = identity.BLegID
	}
	if identity.ObservedAt.IsZero() {
		identity.ObservedAt = time.Unix(0, 0).UTC()
	} else {
		identity.ObservedAt = identity.ObservedAt.UTC()
	}
	if identity.ReceivedAt.IsZero() {
		identity.ReceivedAt = identity.ObservedAt
	} else {
		identity.ReceivedAt = identity.ReceivedAt.UTC()
	}
	seq := identity.AttemptSeq
	if seq == 0 {
		seq = 1
	}
	return []aipsdkmetering.Observation{
		newBoundaryObservation(aipsdkmetering.BoundaryBackendEgress, aipsdkmetering.PerspectiveOperator, identity, inputMeasures(s.Input), seq),
		newBoundaryObservation(aipsdkmetering.BoundaryBackendIngress, aipsdkmetering.PerspectiveOperator, identity, outputMeasures(s.ProviderOutput, false), seq),
		newBoundaryObservation(aipsdkmetering.BoundaryFrontendEgress, aipsdkmetering.PerspectiveCustomer, identity, outputMeasures(s.CustomerOutput, true), seq),
	}
}

func newBoundaryObservation(boundary aipsdkmetering.Boundary, perspective aipsdkmetering.EconomicPerspective, identity ObservationIdentity, measures []aipsdkmetering.Measure, seq uint64) aipsdkmetering.Observation {
	attempt := identity.AttemptID
	if attempt == "" {
		attempt = identity.BLegID
	}
	streamID := "b-leg:" + identity.BLegID + ":local:" + string(boundary)
	sourceKey := "local-boundary:" + string(boundary) + ":" + attempt
	if len(sourceKey) > aipsdkmetering.MaxSourceEventKeyBytes {
		sourceKey = sourceKey[:aipsdkmetering.MaxSourceEventKeyBytes]
	}
	seed := strings.Join([]string{identity.StoreID, identity.BillingCallID, identity.BLegID, attempt, string(boundary), BoundaryMappingRef}, "\x00")
	hash := sha256.Sum256([]byte(seed))
	id := "aip-local-boundary-" + hex.EncodeToString(hash[:])
	aLegID := identity.ALegID
	callID := identity.CallID
	observation := aipsdkmetering.Observation{
		Version:        aipsdkmetering.ObservationVersionV2,
		ID:             id,
		SourceEventKey: sourceKey,
		Revision:       1,
		StreamID:       streamID,
		Sequence:       seq,
		Origin:         aipsdkmetering.OriginLocal,
		Acquisition:    aipsdkmetering.AcquisitionLocalMeasurement,
		Authority:      boundaryAuthority(measures),
		Perspective:    perspective,
		Boundary:       boundary,
		Lifecycle:      aipsdkmetering.LifecycleBackendAttempt,
		Subject: aipsdkmetering.SubjectRef{
			Kind: aipsdkmetering.SubjectBLeg, StoreID: identity.StoreID, RequestID: identity.RequestID,
			CallID: callID, BillingCallID: identity.BillingCallID, ALegID: aLegID,
			BLegID: identity.BLegID, AttemptID: attempt, AttemptSeq: identity.AttemptSeq,
		},
		Correlation: aipsdkmetering.CorrelationV2{
			StoreID: identity.StoreID, RequestID: identity.RequestID, CallID: callID,
			BillingCallID: identity.BillingCallID, ALegID: aLegID, BLegID: identity.BLegID,
			AttemptID: attempt, AttemptSeq: identity.AttemptSeq,
		},
		Scope: identity.Scope.Clone(), Semantics: aipsdkmetering.SemanticsCumulative,
		ObservedAt: identity.ObservedAt, ReceivedAt: identity.ReceivedAt,
		MappingRef: BoundaryMappingRef, Measures: measures,
	}
	return observation
}

func boundaryAuthority(measures []aipsdkmetering.Measure) string {
	anyValue, anyObserved, anyEstimated := false, false, false
	for _, measure := range measures {
		if measure.Value != nil {
			anyValue = true
			switch measure.Quality {
			case aipsdkmetering.QualityObserved:
				anyObserved = true
			case aipsdkmetering.QualityEstimated:
				anyEstimated = true
			}
		}
	}
	switch {
	case anyObserved:
		return aipsdkmetering.AuthorityObservedClaim
	case anyEstimated:
		return aipsdkmetering.AuthorityEstimatedClaim
	case !anyValue:
		return aipsdkmetering.AuthorityUnavailableClaim
	default:
		return aipsdkmetering.AuthorityUnavailableClaim
	}
}

func inputMeasures(snapshot PreparedInputSnapshot) []aipsdkmetering.Measure {
	measures := make([]aipsdkmetering.Measure, 0, 16)
	measures = appendLifecycleMeasures(measures, snapshot)
	method := boundedMethodRef(snapshot.MethodRef)
	if method == "" {
		method = "local.boundary.input.v1"
	}
	quality := aipsdkmetering.QualityObserved
	reason := ""
	if !snapshot.AdapterProvided || snapshot.Truncated {
		quality = aipsdkmetering.QualityEstimated
		reason = "final provider representation was not fully observed locally"
	}
	if snapshot.TextBytesPresent {
		measures = appendIntegerMeasure(measures, aipsdkmetering.ComponentKey{Direction: aipsdkmetering.DirectionInput, Component: "text_bytes", Unit: aipsdkmetering.UnitByte, SchemaID: BoundaryMappingRef}, snapshot.TextBytes, quality, method, reason)
	}
	if snapshot.TextTokensPresent {
		tokenQuality := aipsdkmetering.QualityObserved
		tokenMethod := method
		tokenReason := ""
		if snapshot.TextTokensEstimated || snapshot.TextTruncated || !snapshot.AdapterProvided {
			tokenQuality = aipsdkmetering.QualityEstimated
			tokenMethod = "local.boundary.token_estimate.v1"
			tokenReason = "provider tokenizer or exact token unit unavailable"
		}
		measures = appendIntegerMeasure(measures, aipsdkmetering.ComponentKey{Direction: aipsdkmetering.DirectionInput, Component: aipsdkmetering.ComponentTextToken, Unit: aipsdkmetering.UnitToken, SchemaID: BoundaryMappingRef}, snapshot.TextTokens, tokenQuality, tokenMethod, tokenReason)
	}
	if snapshot.PayloadBytesPresent {
		payloadQuality := aipsdkmetering.QualityObserved
		payloadReason := ""
		if snapshot.Truncated {
			payloadQuality = aipsdkmetering.QualityEstimated
			payloadReason = "bounded payload metadata was truncated"
		}
		measures = appendIntegerMeasure(measures, aipsdkmetering.ComponentKey{Direction: aipsdkmetering.DirectionInput, Component: "payload_bytes", Unit: aipsdkmetering.UnitByte, SchemaID: BoundaryMappingRef}, snapshot.PayloadBytes, payloadQuality, method, payloadReason)
	}
	mediaQuality := quality
	mediaReason := reason
	if snapshot.MediaTruncated {
		mediaQuality = aipsdkmetering.QualityEstimated
		mediaReason = "bounded media metadata was truncated"
	}
	measures = appendMediaMeasures(measures, snapshot.Media, aipsdkmetering.DirectionInput, mediaQuality, method, mediaReason)
	return appendUnavailableInput(measures)
}

// appendLifecycleMeasures carries the local backend-open state in the durable
// V2 input observation. These request-count measures are qualified by state;
// they are evidence facts, not provider units or monetary quantities.
func appendLifecycleMeasures(existing []aipsdkmetering.Measure, snapshot PreparedInputSnapshot) []aipsdkmetering.Measure {
	method := "local.boundary.lifecycle.v1"
	key := func(state string) aipsdkmetering.ComponentKey {
		return aipsdkmetering.ComponentKey{
			Direction: aipsdkmetering.DirectionNone,
			Component: aipsdkmetering.ComponentRequest,
			Unit:      aipsdkmetering.UnitCount,
			SchemaID:  BoundaryMappingRef,
			Dimensions: []aipsdkmetering.Dimension{{
				Name: "state", Value: state,
			}},
		}
	}
	prepared := int64(0)
	if snapshot.Prepared {
		prepared = 1
	}
	existing = appendIntegerMeasure(existing, key("prepared"), prepared, aipsdkmetering.QualityObserved, method, "")
	attempted := int64(0)
	if snapshot.Attempted {
		attempted = 1
	}
	existing = appendIntegerMeasure(existing, key("attempted"), attempted, aipsdkmetering.QualityObserved, method, "")
	if snapshot.AcceptedKnown {
		accepted := int64(0)
		if snapshot.Accepted {
			accepted = 1
		}
		existing = appendIntegerMeasure(existing, key("accepted"), accepted, aipsdkmetering.QualityObserved, method, "")
	} else {
		existing = appendUnavailable(existing, key("accepted"), method, "backend-open acceptance was not observed locally")
	}
	return existing
}

func outputMeasures(snapshot OutputSnapshot, customer bool) []aipsdkmetering.Measure {
	measures := make([]aipsdkmetering.Measure, 0, 16)
	method := "local.boundary.provider-output.v1"
	if customer {
		method = "local.boundary.customer-egress.v1"
	}
	quality := aipsdkmetering.QualityObserved
	reason := ""
	if snapshot.TextTruncated {
		quality = aipsdkmetering.QualityEstimated
		reason = "bounded output text was truncated"
	}
	if snapshot.TextBytesPresent {
		measures = appendIntegerMeasure(measures, aipsdkmetering.ComponentKey{Direction: aipsdkmetering.DirectionOutput, Component: "text_bytes", Unit: aipsdkmetering.UnitByte, SchemaID: BoundaryMappingRef}, snapshot.TextBytes, quality, method, reason)
	}
	if snapshot.TextTokensPresent {
		tokenQuality := aipsdkmetering.QualityEstimated
		tokenReason := "provider tokenizer or exact token unit unavailable"
		if !snapshot.TextTokensEstimated && !snapshot.TextTruncated {
			tokenQuality = aipsdkmetering.QualityObserved
			tokenReason = ""
		}
		measures = appendIntegerMeasure(measures, aipsdkmetering.ComponentKey{Direction: aipsdkmetering.DirectionOutput, Component: aipsdkmetering.ComponentTextToken, Unit: aipsdkmetering.UnitToken, SchemaID: BoundaryMappingRef}, snapshot.TextTokens, tokenQuality, "local.boundary.token_estimate.v1", tokenReason)
	}
	mediaQuality := aipsdkmetering.QualityObserved
	mediaReason := ""
	if snapshot.MediaTruncated {
		mediaQuality = aipsdkmetering.QualityEstimated
		mediaReason = "bounded media metadata was truncated"
	}
	measures = appendMediaMeasures(measures, snapshot.Media, aipsdkmetering.DirectionOutput, mediaQuality, method, mediaReason)
	return appendUnavailableOutput(measures)
}

func appendIntegerMeasure(existing []aipsdkmetering.Measure, key aipsdkmetering.ComponentKey, value int64, quality, method, reason string) []aipsdkmetering.Measure {
	// Leave room for the fixed unavailable-component declarations appended to
	// every local plane. This keeps a fully populated bounded media capture
	// below the SDK envelope's measure bound instead of dropping the envelope.
	if len(existing) >= aipsdkmetering.MaxObservationMeasures-8 {
		return existing
	}
	if value < 0 {
		return existing
	}
	method = boundedMethodRef(method)
	reason = boundedMethodRef(reason)
	canonical := key.CanonicalKey()
	if canonical == "" {
		return existing
	}
	for i := range existing {
		if existing[i].Key.CanonicalKey() != canonical || existing[i].Value == nil {
			continue
		}
		old, err := strconv.ParseInt(existing[i].Value.Coefficient, 10, 64)
		if err != nil {
			continue
		}
		if old > maxBoundaryValue-value {
			old = maxBoundaryValue
		} else {
			old += value
		}
		valueCopy := decimalForInteger(old)
		existing[i].Value = &valueCopy
		if existing[i].Quality == aipsdkmetering.QualityEstimated && quality == aipsdkmetering.QualityObserved {
			existing[i].Quality = quality
			existing[i].MethodRef = method
			existing[i].Reason = reason
		}
		return existing
	}
	valueCopy := decimalForInteger(value)
	return append(existing, aipsdkmetering.Measure{Key: key, Value: &valueCopy, Quality: quality, MethodRef: method, Reason: reason})
}

func decimalForInteger(value int64) aipsdkmetering.Decimal {
	if value < 0 {
		value = 0
	}
	return aipsdkmetering.Decimal{Coefficient: strconv.FormatInt(value, 10)}
}

func appendMediaMeasures(existing []aipsdkmetering.Measure, media []MediaSummary, direction aipsdkmetering.FlowDirection, quality, method, reason string) []aipsdkmetering.Measure {
	for _, item := range media {
		if !item.Kind.valid() {
			continue
		}
		dimensions := mediaDimensions(item)
		component := string(item.Kind)
		if item.Count > 0 {
			existing = appendIntegerMeasure(existing, aipsdkmetering.ComponentKey{Direction: direction, Component: component, Unit: mediaCountUnit(item.Kind), SchemaID: BoundaryMappingRef, Dimensions: dimensions}, item.Count, quality, method, reason)
		}
		if item.BytesPresent {
			existing = appendIntegerMeasure(existing, aipsdkmetering.ComponentKey{Direction: direction, Component: component, Unit: aipsdkmetering.UnitByte, SchemaID: BoundaryMappingRef, Dimensions: dimensions}, item.Bytes, quality, method, reason)
		}
		if item.DurationPresent {
			existing = appendIntegerMeasure(existing, aipsdkmetering.ComponentKey{Direction: direction, Component: component, Unit: aipsdkmetering.UnitMillisecond, SchemaID: BoundaryMappingRef, Dimensions: dimensions}, item.DurationMillis, quality, method, reason)
		}
		if item.FramesPresent {
			existing = appendIntegerMeasure(existing, aipsdkmetering.ComponentKey{Direction: direction, Component: component, Unit: aipsdkmetering.UnitFrame, SchemaID: BoundaryMappingRef, Dimensions: dimensions}, item.Frames, quality, method, reason)
		}
		if item.PagesPresent {
			existing = appendIntegerMeasure(existing, aipsdkmetering.ComponentKey{Direction: direction, Component: component, Unit: aipsdkmetering.UnitPage, SchemaID: BoundaryMappingRef, Dimensions: dimensions}, item.Pages, quality, method, reason)
		}
		if item.WidthPresent {
			existing = appendIntegerMeasure(existing, aipsdkmetering.ComponentKey{Direction: direction, Component: component, Unit: aipsdkmetering.UnitPixel, SchemaID: BoundaryMappingRef, Dimensions: appendMediaAxis(dimensions, "width")}, item.WidthPixels, quality, method, reason)
		}
		if item.HeightPresent {
			existing = appendIntegerMeasure(existing, aipsdkmetering.ComponentKey{Direction: direction, Component: component, Unit: aipsdkmetering.UnitPixel, SchemaID: BoundaryMappingRef, Dimensions: appendMediaAxis(dimensions, "height")}, item.HeightPixels, quality, method, reason)
		}
	}
	return existing
}

func mediaCountUnit(kind MediaKind) string {
	switch kind {
	case MediaImage:
		return aipsdkmetering.UnitImage
	case MediaAudio:
		return aipsdkmetering.UnitAudio
	case MediaVideo:
		return aipsdkmetering.UnitVideo
	case MediaDocument:
		return aipsdkmetering.UnitDocument
	default:
		return aipsdkmetering.UnitFile
	}
}

func mediaDimensions(item MediaSummary) []aipsdkmetering.Dimension {
	if item.MIME == "" {
		return nil
	}
	return []aipsdkmetering.Dimension{{Name: "mime", Value: boundedMIME(item.MIME)}}
}

func appendMediaAxis(dimensions []aipsdkmetering.Dimension, axis string) []aipsdkmetering.Dimension {
	out := make([]aipsdkmetering.Dimension, 0, len(dimensions)+1)
	out = append(out, dimensions...)
	out = append(out, aipsdkmetering.Dimension{Name: "axis", Value: axis})
	return out
}

func appendUnavailableInput(existing []aipsdkmetering.Measure) []aipsdkmetering.Measure {
	method := "local.boundary.unobservable.v1"
	existing = appendUnavailable(existing, aipsdkmetering.ComponentKey{Direction: aipsdkmetering.DirectionInput, Component: aipsdkmetering.ComponentCacheReadInputToken, Unit: aipsdkmetering.UnitToken, SchemaID: BoundaryMappingRef}, method, "provider cache disposition is not observable locally")
	existing = appendUnavailable(existing, aipsdkmetering.ComponentKey{Direction: aipsdkmetering.DirectionInput, Component: aipsdkmetering.ComponentCacheWriteInputToken, Unit: aipsdkmetering.UnitToken, SchemaID: BoundaryMappingRef}, method, "provider cache disposition is not observable locally")
	existing = appendUnavailableOutput(existing)
	return existing
}

func appendUnavailableOutput(existing []aipsdkmetering.Measure) []aipsdkmetering.Measure {
	method := "local.boundary.unobservable.v1"
	existing = appendUnavailable(existing, aipsdkmetering.ComponentKey{Direction: aipsdkmetering.DirectionOutput, Component: aipsdkmetering.ComponentReasoningOutputToken, Unit: aipsdkmetering.UnitToken, SchemaID: BoundaryMappingRef}, method, "hidden provider reasoning is not observable locally")
	existing = appendUnavailable(existing, aipsdkmetering.ComponentKey{Direction: aipsdkmetering.DirectionNone, Component: aipsdkmetering.ComponentToolQuery, Unit: aipsdkmetering.UnitCount, SchemaID: BoundaryMappingRef}, method, "provider tool execution is not observable locally")
	existing = appendUnavailable(existing, aipsdkmetering.ComponentKey{Direction: aipsdkmetering.DirectionNone, Component: "compute_time", Unit: aipsdkmetering.UnitMillisecond, SchemaID: BoundaryMappingRef}, method, "provider compute is not observable locally")
	return existing
}

func appendUnavailable(existing []aipsdkmetering.Measure, key aipsdkmetering.ComponentKey, method, reason string) []aipsdkmetering.Measure {
	if key.CanonicalKey() == "" {
		return existing
	}
	return append(existing, aipsdkmetering.Measure{Key: key, Quality: aipsdkmetering.QualityUnavailable, MethodRef: boundedMethodRef(method), Reason: boundedMethodRef(reason)})
}
