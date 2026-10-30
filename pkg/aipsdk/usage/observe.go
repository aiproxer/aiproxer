package usage

import (
	"context"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// Event is a provider-neutral usage observation emitted from canonical usage deltas.
type Event struct {
	TraceID     string
	ALegID      string
	BLegID      string
	PrincipalID string
	SessionID   string
	AttemptSeq  int
	BackendID   string
	FrontendID  string
	Model       string

	// StructuralCause is the proxy-owned, content-free label of a structural
	// change the proxy applied to the request this leg belongs to, for example
	// a compaction rewrite. It is a bounded diagnostic token, never
	// content, and observers that do not consume it ignore it. It rides the
	// leg's own usage sample, so an observer attributes the change to exactly
	// the request that carried it. The request publishes it at most once per
	// cache key, so a later leg of the same request carries no cause.
	StructuralCause string

	// SeamDigests carries content-free per-item hashes of the model-bound
	// request that produced this leg, oldest trajectory entry first. Each
	// entry is a fixed-size HMAC-SHA256 over one normalized request item
	// under a process-stable secret that is never logged, persisted, or
	// exposed; observers receive hashes only, never request content and
	// never the key, and the collapse diagnostic retains at most its
	// configured bound. Empty when the feed is disabled or the leg has no
	// canonical call (for example the wire/large-body path). Observers that
	// do not consume it ignore it.
	//
	// Privacy: the digests are keyed fingerprints, not content, and they are
	// not offline dictionary-matchable without the process key. Observers
	// must still treat them as sensitive diagnostic evidence: do not log,
	// persist, exfiltrate, retain, or mutate them. The collapse diagnostic
	// copies what it retains.
	SeamDigests [][32]byte

	// SystemPromptDigest is the proxy-owned, content-free identity of the
	// effective provider-bound system/developer instructions of the request
	// this leg belongs to, including their placement in the trajectory: an
	// HMAC-SHA256 over each instruction's position and normalized content
	// under the same process-stable key that protects SeamDigests. The zero
	// value means no identity was observed for this leg (the feed is
	// disabled, the leg has no canonical call, or the executor carried no
	// process key). Production never publishes a zero digest, so zero is
	// unambiguous.
	//
	// The prompt-cache collapse diagnostic compares the identity of the leg
	// that triggers an episode with the identity of the healthy sample that
	// anchors it. A difference means the effective system or developer
	// instruction, including its placement, changed between those two
	// requests — for example a steering write that lands between turns — and
	// the episode is attributed to system-prompt. A change to user-role
	// steering content (the thinker memo is a user message) is not a
	// system-prompt change and stays unattributed.
	//
	// Instruction content never crosses this seam: observers receive one
	// keyed fingerprint, never instruction text. It is process-stable
	// diagnostic evidence of the same class as SeamDigests: an observer that
	// retains usage events (the opt-in reference transcript ledger does)
	// holds the fingerprint for as long as it holds the event, and must never
	// log, persist, or expose it. Observers that do not consume it ignore it.
	SystemPromptDigest [32]byte

	// PromptCacheLaunchSeq is the process-owned, strictly increasing launch
	// ordinal of the request this leg belongs to. The executor assigns it when
	// it accepts the request, before any candidate attempt starts, and every
	// leg of that request carries the same value. The sequence lives with the
	// process and is shared by every generation executor, so a configuration
	// reload or an overlapping retired generation does not restart it.
	//
	// It lets the collapse diagnostic compare the healthy instruction and
	// switch references with the collapse-triggering leg by request launch
	// order. Usage observations can arrive out of order (a slow older request
	// completing after a newer one), so a difference in identity alone cannot
	// prove that the triggering request started later. A zero value means the
	// ordinal is unavailable (the diagnostic identity feeds are disabled, the
	// observation came from a non-executor producer, or the path lost the
	// request context); the diagnostic then stays conservative and does not
	// attribute a cross-request identity change. The value is a process-local
	// counter, never content, and it is published only together with the
	// prompt-cache diagnostic identity feeds.
	PromptCacheLaunchSeq uint64

	// CacheIdentityDigest is the proxy-owned, keyed, content-free identity of
	// the core-visible downstream provider choice for the request this leg
	// belongs to: the configured backend and the resolved native model. It is
	// a fixed-size HMAC-SHA256 digest under the process-stable diagnostic
	// key, never a raw backend, model, endpoint, or provider identifier.
	//
	// The zero value means the diagnostic identity feed was disabled, the
	// executor had no process key, or the attempt did not reach a committed
	// open observation. A zero value is never comparable: production either
	// publishes a keyed value or publishes nothing rather than a publicly
	// reproducible fingerprint.
	//
	// The prompt-cache collapse diagnostic compares the evidence of the leg
	// that triggers an episode with the evidence of the healthy sample that
	// anchors it. A core identity change means the effective backend or
	// native model changed between those two requests. A temporary change
	// that reverts before the trigger leaves the identities equal and is not
	// blamed.
	//
	// Privacy: this is content-free diagnostic evidence of the same class as
	// SeamDigests. An observer that retains usage events (the opt-in
	// reference transcript ledger does) holds the digest for as long as it
	// holds the event, and must never log, persist, or expose it separately.
	CacheIdentityDigest [32]byte

	// CacheTargetDigest is the optional adapter-reported downstream cache
	// residency target for this leg (for example endpoint, region, service
	// tier, or provider-issued affinity identity), keyed and content-free.
	//
	// The zero value means the adapter reported no target. Unknown is not a
	// known target: the collapse diagnostic compares this component only when
	// both the healthy reference and the triggering leg carry a non-zero
	// target digest. An unknown-to-known or known-to-unknown transition is
	// therefore not, by itself, evidence of a target switch. A core identity
	// change remains comparable independently of this component.
	CacheTargetDigest [32]byte

	// CredentialIdentityDigest is the optional keyed identity of the
	// credential selected when the backend was opened, when the adapter
	// reports one. It is never credential material or a raw account
	// identifier, and the zero value means no open-time credential was
	// reported. The diagnostic compares it only when both sides carry this
	// component.
	CredentialIdentityDigest [32]byte

	// ProviderAccountDigest is the optional keyed identity of the provider
	// account carried by this specific usage sample's accounting metadata,
	// when the adapter reports one. It lets connectors that expose account
	// identity only in usage (not at open) participate in credential
	// attribution. The zero value means this sample carried no account key.
	// The diagnostic compares it only when both the healthy reference and the
	// trigger carry this component, and an open-time credential and a usage
	// account are separate evidence: neither is combined with the other into
	// an ambiguous composite.
	//
	// Provider account keys are often low-entropy slot identifiers, so this
	// is HMAC-SHA256 under the process-stable diagnostic key, never an
	// ordinary hash. A keyless executor publishes zero evidence.
	ProviderAccountDigest [32]byte

	// Scope is the optional safe principal/scope attribution snapshot for this request. It is
	// additive metadata; existing observers may ignore it and keep reading PrincipalID
	// (requirements 6.4, 6.5, 7.3). When Scope.PrincipalID is known it must match PrincipalID.
	Scope scope.PrincipalScopeView

	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
	ReasoningTokens  int
	TotalTokens      int

	CostNanoUnits int64
	Currency      string
	CostSource    string
	RawUsageJSON  string

	RecordedAt time.Time
}

// Observer records usage without mutating request or stream execution.
type Observer interface {
	OnUsage(ctx context.Context, ev Event) error
}

// NoopObserver drops usage events.
type NoopObserver struct{}

func (NoopObserver) OnUsage(context.Context, Event) error { return nil }

// ChainObserver fans usage events out to observers in registration order.
// Each observer receives an isolated copy of the digest slice, so a
// mutating observer cannot corrupt another observer's diagnostic evidence
// through the shared backing array. Error propagation and order are
// unchanged: the first error stops the chain.
type ChainObserver struct {
	observers []Observer
}

func (c ChainObserver) OnUsage(ctx context.Context, ev Event) error {
	for _, o := range c.observers {
		delivery := ev
		if len(c.observers) > 1 && len(delivery.SeamDigests) > 0 {
			isolated := make([][32]byte, len(delivery.SeamDigests))
			copy(isolated, delivery.SeamDigests)
			delivery.SeamDigests = isolated
		}
		if err := o.OnUsage(ctx, delivery); err != nil {
			return err
		}
	}
	return nil
}

var (
	_ Observer = NoopObserver{}
	_ Observer = ChainObserver{}
)

// ChainObservers returns a [ChainObserver] over non-nil observers (registration order preserved).
// An empty chain is a no-op observer.
func ChainObservers(observers ...Observer) ChainObserver {
	filtered := make([]Observer, 0, len(observers))
	for _, o := range observers {
		if o != nil {
			filtered = append(filtered, o)
		}
	}
	return ChainObserver{observers: filtered}
}
