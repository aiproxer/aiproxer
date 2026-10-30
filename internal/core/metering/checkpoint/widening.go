package checkpoint

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"strconv"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// fingerprintBufferBytes bounds the scratch space a billable fingerprint keeps
// live. A fixed buffer lets the fingerprint stream the preimage into SHA-256
// instead of materializing a second copy of the whole agent history.
const fingerprintBufferBytes = 512

// fingerprintWriter streams the exact billable preimage into SHA-256 through a
// fixed scratch buffer. The preimage is bounded only by request limits, so
// building it in a strings.Builder made each attempt allocate several times the
// history size (geometric builder growth plus the final []byte copy). Every
// write below keeps the same byte order as the former builder so existing
// fingerprints stay stable.
type fingerprintWriter struct {
	h   hash.Hash
	buf [fingerprintBufferBytes]byte
	n   int
}

func (w *fingerprintWriter) writeString(s string) {
	for len(s) > 0 {
		if w.n == len(w.buf) {
			w.flush()
		}
		copied := copy(w.buf[w.n:], s)
		w.n += copied
		s = s[copied:]
	}
}

func (w *fingerprintWriter) writeBytes(p []byte) {
	for len(p) > 0 {
		if w.n == len(w.buf) {
			w.flush()
		}
		copied := copy(w.buf[w.n:], p)
		w.n += copied
		p = p[copied:]
	}
}

func (w *fingerprintWriter) writeByte(b byte) {
	if w.n == len(w.buf) {
		w.flush()
	}
	w.buf[w.n] = b
	w.n++
}

func (w *fingerprintWriter) flush() {
	if w.n == 0 {
		return
	}
	_, _ = w.h.Write(w.buf[:w.n])
	w.n = 0
}

func (w *fingerprintWriter) hexDigest() []byte {
	w.flush()
	sum := w.h.Sum(nil)
	out := make([]byte, hex.EncodedLen(len(sum)))
	hex.Encode(out, sum)
	return out
}

// BillableFingerprint is a stable encoding of billable call content used to
// detect unmeasured widening after an authorized backend-ingress freeze.
// It avoids json.Marshal of json.RawMessage fields (tool Parameters) so invalid
// or non-JSON parameter bytes do not fail Open.
func BillableFingerprint(c aipapi.Call) ([]byte, error) {
	var w fingerprintWriter
	w.h = sha256.New()
	writeMessages(&w, "I", c.Instructions)
	writeMessages(&w, "M", c.Messages)
	for i, tool := range c.Tools {
		w.writeString("|T")
		w.writeString(strconv.Itoa(i))
		w.writeByte('=')
		w.writeString(tool.Name)
		w.writeByte(':')
		w.writeBytes(tool.Parameters)
	}
	w.writeString("|TC=")
	w.writeString(string(c.ToolChoice.Mode))
	// MaxOutputTokens is compared separately so authority clamps may narrow the
	// bound after freeze without failing the no-widening invariant (req 7.5).
	if c.Options.Temperature != nil {
		w.writeString("|TP=")
		w.writeString(strconv.FormatFloat(*c.Options.Temperature, 'g', -1, 64))
	}
	if c.Options.TopP != nil {
		w.writeString("|P=")
		w.writeString(strconv.FormatFloat(*c.Options.TopP, 'g', -1, 64))
	}
	return w.hexDigest(), nil
}

func maxOutputTokensOrNeg(c aipapi.Call) int {
	if c.Options.MaxOutputTokens == nil {
		return -1
	}
	return *c.Options.MaxOutputTokens
}

func writeMessages(w *fingerprintWriter, tag string, msgs []aipapi.Message) {
	for i, m := range msgs {
		w.writeString("|")
		w.writeString(tag)
		w.writeString(strconv.Itoa(i))
		w.writeByte('=')
		w.writeString(string(m.Role))
		for j, p := range m.Parts {
			w.writeByte('#')
			w.writeString(strconv.Itoa(j))
			w.writeByte(':')
			w.writeString(string(p.Kind))
			w.writeByte(':')
			w.writeString(p.Text)
			if p.Kind == aipapi.PartReasoning && p.Reasoning != nil {
				w.writeString(":R")
				writeLenFrame(w, string(p.Reasoning.Dialect))
				writeLenFrame(w, p.Reasoning.Text)
				writeLenFrame(w, p.Reasoning.Signature)
				writeLenFrameBytes(w, p.Reasoning.Opaque)
			}
			if len(p.Content) > 0 {
				w.writeByte(':')
				w.writeBytes(p.Content)
			}
		}
	}
}

func writeLenFrame(w *fingerprintWriter, s string) {
	w.writeString(strconv.Itoa(len(s)))
	w.writeByte(':')
	w.writeString(s)
}

func writeLenFrameBytes(w *fingerprintWriter, p []byte) {
	w.writeString(strconv.Itoa(len(p)))
	w.writeByte(':')
	w.writeBytes(p)
}

// MaxOutputTokensWidened reports whether curMO widens authMO.
// A nil pointer represents an unbounded (infinite) max output token limit.
// Lowering MaxOutputTokens is narrowing, not widening.
// Raising MaxOutputTokens or removing an authorized bound (making it nil/unbounded) is widening.
// Introducing a bound when authorized had none (nil -> non-nil) is narrowing, not widening.
func MaxOutputTokensWidened(authMO, curMO *int) bool {
	authVal := -1
	if authMO != nil {
		authVal = *authMO
	}
	curVal := -1
	if curMO != nil {
		curVal = *curMO
	}
	return maxOutputTokensIntWidened(authVal, curVal)
}

func maxOutputTokensIntWidened(authVal, curVal int) bool {
	switch {
	case authVal < 0 && curVal < 0:
		return false
	case authVal < 0 && curVal >= 0:
		// Freeze had unbounded output; binding a max is not billable content widening.
		return false
	case authVal >= 0 && curVal < 0:
		// Freeze had bounded output; removing the bound is unmeasured widening.
		return true
	case curVal > authVal:
		// Bound increased beyond authorized bound.
		return true
	default:
		return false
	}
}

// BillableWidened reports whether current has billable content beyond authorized.
// Lowering MaxOutputTokens (authority/preflight clamp) is narrowing, not widening.
// Raising MaxOutputTokens or removing an authorized bound is widening; introducing
// a bound when the freeze had none is narrowing, not widening.
func BillableWidened(authorized, current aipapi.Call) (bool, error) {
	a, err := BillableFingerprint(authorized)
	if err != nil {
		return false, err
	}
	b, err := BillableFingerprint(current)
	if err != nil {
		return false, err
	}
	if !bytes.Equal(a, b) {
		return true, nil
	}
	authMO := maxOutputTokensOrNeg(authorized)
	curMO := maxOutputTokensOrNeg(current)
	return maxOutputTokensIntWidened(authMO, curMO), nil
}

// ErrUnmeasuredWidening is returned when a call changes billable content after
// the authorized backend-ingress freeze (requirement 7.5).
var ErrUnmeasuredWidening = fmt.Errorf("metering/checkpoint: unmeasured post-authorization widening")

// AssertNotWidened returns ErrUnmeasuredWidening when current differs from authorized.
func AssertNotWidened(authorized, current aipapi.Call) error {
	widened, err := BillableWidened(authorized, current)
	if err != nil {
		return err
	}
	if widened {
		return ErrUnmeasuredWidening
	}
	return nil
}

// WireAttemptEvidence captures bounded cryptographic and quantity evidence
// for an attempt on the wire path, used to assert integrity and detect widening
// without retaining or re-reading prompt trees (Requirements 10, 15.3, 19).
type WireAttemptEvidence struct {
	SourceDigest    [32]byte
	RewriteDigest   [32]byte
	AttemptDigest   [32]byte
	Model           string
	MaxOutputTokens *int
}

// ComputeAttemptDigest derives a deterministic attempt digest from the source digest,
// rewrite digest, and effective model name (Requirements 10, 15.3, 19).
func ComputeAttemptDigest(sourceDigest, rewriteDigest [32]byte, model string) [32]byte {
	h := sha256.New()
	h.Write(sourceDigest[:])
	h.Write(rewriteDigest[:])
	h.Write([]byte(strings.TrimSpace(model)))
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

// ComputeRewriteDigest derives a deterministic rewrite digest for a model replacement
// token and span, or returns a zero digest if there is no rewrite (Requirements 9, 15.3).
func ComputeRewriteDigest(offset, length int64, replacementToken string) [32]byte {
	if length <= 0 && replacementToken == "" {
		return [32]byte{}
	}
	h := sha256.New()
	var buf [16]byte
	binary.BigEndian.PutUint64(buf[0:8], uint64(offset))
	binary.BigEndian.PutUint64(buf[8:16], uint64(length))
	h.Write(buf[:])
	h.Write([]byte(replacementToken))
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

// BillableWireWidened reports whether current wire attempt evidence has widened beyond authorized.
func BillableWireWidened(authorized, current WireAttemptEvidence) (bool, error) {
	if authorized.SourceDigest != current.SourceDigest {
		return true, nil
	}
	if authorized.RewriteDigest != current.RewriteDigest {
		return true, nil
	}
	if authorized.AttemptDigest != current.AttemptDigest {
		return true, nil
	}
	if strings.TrimSpace(authorized.Model) != strings.TrimSpace(current.Model) {
		return true, nil
	}
	if MaxOutputTokensWidened(authorized.MaxOutputTokens, current.MaxOutputTokens) {
		return true, nil
	}
	return false, nil
}

// AssertWireNotWidened returns ErrUnmeasuredWidening when current wire attempt evidence
// differs from authorized or has widened max output tokens (Requirements 10, 15.3, 19).
func AssertWireNotWidened(authorized, current WireAttemptEvidence) error {
	widened, err := BillableWireWidened(authorized, current)
	if err != nil {
		return err
	}
	if widened {
		return ErrUnmeasuredWidening
	}
	return nil
}
