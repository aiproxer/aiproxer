package conversationprojection

import (
	"fmt"
	"strconv"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// MessageAnchor pairs a replay-stable message identity with an explicit occurrence ordinal.
type MessageAnchor struct {
	Identity   MessageIdentity `json:"identity"`
	Occurrence uint32          `json:"occurrence"`
}

// Validate checks that the anchor has a valid v1 identity and a non-zero occurrence ordinal.
func (a MessageAnchor) Validate() error {
	if err := a.Identity.Validate(); err != nil {
		return err
	}
	if a.Occurrence == 0 {
		return fmt.Errorf("%w: occurrence ordinal must be >= 1", ErrInvalidMessageAnchor)
	}
	return nil
}

// String returns the formatted anchor representation "v1:<hex>#<occurrence>".
func (a MessageAnchor) String() string {
	return string(a.Identity) + "#" + strconv.FormatUint(uint64(a.Occurrence), 10)
}

// ComputeItemAnchors computes occurrence-indexed MessageAnchors for all message items in items.
// Non-message items (e.g. tool calls, tool results) are skipped in the resulting anchor slice.
func ComputeItemAnchors(items []aipapi.Item) ([]MessageAnchor, error) {
	counts := make(map[MessageIdentity]uint32)
	anchors := make([]MessageAnchor, 0, len(items))

	for _, item := range items {
		if item.Kind != aipapi.ItemKindMessage {
			continue
		}
		id, err := ItemIdentityOf(item)
		if err != nil {
			return nil, err
		}
		counts[id]++
		anchors = append(anchors, MessageAnchor{
			Identity:   id,
			Occurrence: counts[id],
		})
	}

	return anchors, nil
}

// ComputeCallAnchors computes occurrence-indexed MessageAnchors for all message units in call,
// supporting both item authority (Items) and legacy authority (Instructions + Messages).
func ComputeCallAnchors(call aipapi.Call) ([]MessageAnchor, error) {
	if call.HasItemAuthority() {
		return ComputeItemAnchors(call.Items)
	}

	counts := make(map[MessageIdentity]uint32)
	totalMsgs := len(call.Instructions) + len(call.Messages)
	anchors := make([]MessageAnchor, 0, totalMsgs)

	for _, msg := range call.Instructions {
		id, err := MessageIdentityOf(msg)
		if err != nil {
			return nil, err
		}
		counts[id]++
		anchors = append(anchors, MessageAnchor{
			Identity:   id,
			Occurrence: counts[id],
		})
	}

	for _, msg := range call.Messages {
		id, err := MessageIdentityOf(msg)
		if err != nil {
			return nil, err
		}
		counts[id]++
		anchors = append(anchors, MessageAnchor{
			Identity:   id,
			Occurrence: counts[id],
		})
	}

	return anchors, nil
}

// ItemAnchorAt returns the MessageAnchor for the message item at targetIndex in items.
// If targetIndex is out of range, an error is returned.
// If the item at targetIndex is not ItemKindMessage, ErrNonMessageItem is returned.
func ItemAnchorAt(items []aipapi.Item, targetIndex int) (MessageAnchor, error) {
	if targetIndex < 0 || targetIndex >= len(items) {
		return MessageAnchor{}, fmt.Errorf("index %d out of range [0, %d)", targetIndex, len(items))
	}
	targetItem := items[targetIndex]
	if targetItem.Kind != aipapi.ItemKindMessage {
		return MessageAnchor{}, fmt.Errorf("%w: item at index %d has kind %q", ErrNonMessageItem, targetIndex, targetItem.Kind)
	}

	targetID, err := ItemIdentityOf(targetItem)
	if err != nil {
		return MessageAnchor{}, err
	}

	var count uint32
	for i := 0; i <= targetIndex; i++ {
		if items[i].Kind != aipapi.ItemKindMessage {
			continue
		}
		id, err := ItemIdentityOf(items[i])
		if err != nil {
			return MessageAnchor{}, err
		}
		if id == targetID {
			count++
		}
	}

	return MessageAnchor{
		Identity:   targetID,
		Occurrence: count,
	}, nil
}

// ResolveAnchor locates the 0-based item index matching the anchor in items.
// If found, returns (index, true, nil). If not found, returns (-1, false, nil).
func ResolveAnchor(items []aipapi.Item, anchor MessageAnchor) (int, bool, error) {
	if err := anchor.Validate(); err != nil {
		return -1, false, err
	}

	var count uint32
	for i, item := range items {
		if item.Kind != aipapi.ItemKindMessage {
			continue
		}
		id, err := ItemIdentityOf(item)
		if err != nil {
			return -1, false, err
		}
		if id == anchor.Identity {
			count++
			if count == anchor.Occurrence {
				return i, true, nil
			}
		}
	}

	return -1, false, nil
}
