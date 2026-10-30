package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/internal/core/localstream"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/localturn"
)

// localTurnOutcome is the result of the two-phase local-turn stage.
type localTurnOutcome struct {
	claimed      bool
	mergedSnap   conversationprojection.Snapshot
	stream       aipapi.EventStream
	handlerID    string
	sourceReason localturn.ReasonCode
}

// localTurnHandlers returns the frozen ordered handler list from the snapshot.
func (e *Executor) localTurnHandlers() []localturn.Handler {
	if e == nil || e.RuntimeSnapshot == nil {
		return nil
	}
	return e.RuntimeSnapshot.LocalTurnHandlersExecution()
}

// normalizedMessageCount returns the number of complete normalized messages
// available for claiming in call. For item authority, count ItemKindMessage;
// for legacy, count Instructions + Messages.
func normalizedMessageCount(call aipapi.Call) int {
	if call.HasItemAuthority() {
		c := 0
		for _, it := range call.Items {
			if it.Kind == aipapi.ItemKindMessage {
				c++
			}
		}
		return c
	}
	return len(call.Instructions) + len(call.Messages)
}

// messageIdentityAt returns the MessageIdentity for the normalized message at idx.
func messageIdentityAt(call aipapi.Call, idx int) (conversationprojection.MessageIdentity, error) {
	if call.HasItemAuthority() {
		n := 0
		for _, it := range call.Items {
			if it.Kind != aipapi.ItemKindMessage {
				continue
			}
			if n == idx {
				return conversationprojection.ItemIdentityOf(it)
			}
			n++
		}
		return "", fmt.Errorf("localturn: index %d out of range", idx)
	}
	combined := 0
	// Instructions first, then Messages (projectLegacy order)
	if idx < len(call.Instructions) {
		return conversationprojection.MessageIdentityOf(call.Instructions[idx])
	}
	combined = len(call.Instructions)
	msgIdx := idx - combined
	if msgIdx < 0 || msgIdx >= len(call.Messages) {
		return "", fmt.Errorf("localturn: index %d out of range", idx)
	}
	return conversationprojection.MessageIdentityOf(call.Messages[msgIdx])
}

// mergeTagsIntoSnapshot returns a new snapshot with Tags from result merged
// without a second store read. It updates StateRevision and NeverBackend.
func mergeTagsIntoSnapshot(base conversationprojection.Snapshot, result TagResult) conversationprojection.Snapshot {
	out := base
	out.StateRevision = result.StateRevision
	// Deep copy tags (already sorted by store)
	tags := make([]conversationprojection.Tag, len(result.Tags))
	copy(tags, result.Tags)
	out.NeverBackend = tags
	return out
}

// localTurnSourceRequests builds TagRequests for claimed source indexes.
func localTurnSourceRequests(call aipapi.Call, res localturn.MatchResult) ([]TagRequest, error) {
	if !res.Claimed {
		return nil, nil
	}
	if len(res.Indexes) == 0 {
		return nil, fmt.Errorf("localturn: claimed with empty indexes")
	}
	out := make([]TagRequest, 0, len(res.Indexes))
	for _, idx := range res.Indexes {
		id, err := messageIdentityAt(call, idx)
		if err != nil {
			return nil, err
		}
		if err := id.Validate(); err != nil {
			return nil, fmt.Errorf("localturn: invalid source identity at %d: %w", idx, err)
		}
		out = append(out, TagRequest{
			Identity: id,
			Reason:   conversationprojection.ReasonCode(res.Reason),
		})
	}
	return out, nil
}

// canonicalReplyMessage constructs the canonical assistant text message for tagging and streaming.
// Delegates to the producer-neutral localstream helper so reply-tag identity
// equals encoded/decoded assistant content across all official frontends.
func canonicalReplyMessage(text string) aipapi.Message {
	return localstream.CanonicalAssistantMessage(text)
}

// buildLocalEventStream constructs a finite EventStream from reply text with
// exactly the same content that was tagged. No provider usage, no B-leg ID,
// no background goroutine; obeys context cancellation/Close.
// Delegates to the generic localstream factory used by frontend contract tests.
func buildLocalEventStream(replyText string) aipapi.EventStream {
	return localstream.NewTextStream(replyText)
}

// localTurnSelection retains the validated pre-snapshot decision, including a
// decline (nil handler), so later preparation never matches against steering.
type localTurnSelection struct {
	handler        localturn.Handler
	match          localturn.MatchResult
	meta           localturn.Meta
	sourceRequests []TagRequest
}

func (e *Executor) selectLocalTurn(ctx context.Context, ingress aipapi.Call, aLegID, traceID string) (localTurnSelection, error) {
	handlers := e.localTurnHandlers()
	if len(handlers) == 0 {
		return localTurnSelection{}, nil
	}
	if e.conversationViewTagger() == nil {
		return localTurnSelection{}, fmt.Errorf("executor: conversation view tagger not available for localturn")
	}
	if strings.TrimSpace(aLegID) == "" {
		return localTurnSelection{}, fmt.Errorf("executor: localturn: missing aLegID")
	}
	msgCount := normalizedMessageCount(ingress)
	meta := localturn.Meta{TraceID: traceID, MessageCount: msgCount}
	// Deterministic order already via MaterializeSorted at snapshot construction,
	// but ensure stable iteration: handlers are already sorted.
	for _, h := range handlers {
		if localturn.IsNilHandler(h) {
			continue
		}
		// Pure Match phase against preserved ingress
		mr, merr := h.Match(ctx, ingress, meta)
		if merr != nil {
			// Pre-claim errors obey fail-open/fail-closed
			if h.FailureMode() == localturn.FailOpen {
				continue
			}
			return localTurnSelection{}, fmt.Errorf("executor: localturn match %s: %w", h.ID(), merr)
		}
		if !mr.Claimed {
			continue
		}
		// Validate claimed result
		if err := mr.Validate(meta); err != nil {
			return localTurnSelection{}, fmt.Errorf("executor: localturn %s invalid match: %w", h.ID(), err)
		}
		// Build source tag requests and validate identities before Handle
		srcReqs, err := localTurnSourceRequests(ingress, mr)
		if err != nil {
			return localTurnSelection{}, fmt.Errorf("executor: localturn %s source identity: %w", h.ID(), err)
		}
		mr.Indexes = append([]int(nil), mr.Indexes...)
		return localTurnSelection{handler: h, match: mr, meta: meta, sourceRequests: srcReqs}, nil
	}
	return localTurnSelection{}, nil
}

// runLocalTurnStage consumes the retained selection at the existing Handle seam.
// Source and reply tagging precede Handle and stream release respectively.
func (e *Executor) runLocalTurnStage(ctx context.Context, ingress aipapi.Call, snap conversationprojection.Snapshot, selection localTurnSelection, tagger ConversationViewTagger, aLegID string) (localTurnOutcome, error) {
	h, mr, meta, srcReqs := selection.handler, selection.match, selection.meta, selection.sourceRequests
	// Persist source tags BEFORE Handle; merge without second store read
	tagRes, err := tagger.TagNeverBackend(ctx, aLegID, srcReqs)
	if err != nil {
		return localTurnOutcome{}, fmt.Errorf("executor: localturn %s source tag: %w", h.ID(), err)
	}
	merged := mergeTagsIntoSnapshot(snap, tagRes)
	// Invoke Handle with panic recovery; after claim any error/panic/invalid reply fails request with no fallback
	var reply localturn.Reply
	var herr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				herr = fmt.Errorf("executor: localturn %s panic: %v", h.ID(), r)
			}
		}()
		reply, herr = h.Handle(ctx, localturn.HandleInput{Call: ingress, Meta: meta, Match: mr})
	}()
	if herr != nil {
		return localTurnOutcome{}, fmt.Errorf("executor: localturn %s handle: %w", h.ID(), herr)
	}
	if err := reply.Validate(); err != nil {
		return localTurnOutcome{}, fmt.Errorf("executor: localturn %s invalid reply: %w", h.ID(), err)
	}
	// Construct canonical assistant message and tag reply BEFORE releasing event
	cmsg := canonicalReplyMessage(reply.Text)
	rid, err := conversationprojection.MessageIdentityOf(cmsg)
	if err != nil {
		return localTurnOutcome{}, fmt.Errorf("executor: localturn %s reply identity: %w", h.ID(), err)
	}
	replyReq := []TagRequest{{Identity: rid, Reason: conversationprojection.ReasonCode(mr.Reason)}}
	replyTagRes, err := tagger.TagNeverBackend(ctx, aLegID, replyReq)
	if err != nil {
		return localTurnOutcome{}, fmt.Errorf("executor: localturn %s reply tag: %w", h.ID(), err)
	}
	merged2 := mergeTagsIntoSnapshot(merged, replyTagRes)
	stream := buildLocalEventStream(reply.Text)
	return localTurnOutcome{
		claimed:      true,
		mergedSnap:   merged2,
		stream:       stream,
		handlerID:    h.ID(),
		sourceReason: mr.Reason,
	}, nil
}
