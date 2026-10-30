package openresponsescompat

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
)

// sseRecord is one parsed SSE record: an optional event discriminator plus the
// accumulated data payload. The data payload is the joined value of every
// "data:" field line ("[DONE]" for the terminal marker).
type sseRecord struct {
	eventType string
	data      []byte
}

// isDONESeRecord reports whether a record data payload is the literal terminal
// marker "[DONE]". The marker is allowed to carry an event field or none; the
// data payload alone is authoritative.
func isDONESeRecord(data []byte) bool {
	return string(bytes.TrimSpace(data)) == "[DONE]"
}

// nextSSERecord parses the next SSE record from br under the pinned profile's
// strict framing rules. Each field line and the total accumulated data payload
// are bounded to maxBytes. A blank line terminates a record; consecutive blank
// lines and comment/ignored fields are skipped. io.EOF is returned only at a
// clean stream boundary (no pending record). A pending record at EOF is flushed
// so truncated JSON payloads are rejected by the caller's decode step.
//
// The record is returned without validating event/body agreement or JSON
// shape; [decodeRemoteStreamEvent] and the stream mapper own those checks.
func nextSSERecord(br *bufio.Reader, maxBytes int) (sseRecord, error) {
	if br == nil {
		return sseRecord{}, errors.New("openresponsescompat: nil SSE reader")
	}
	var rec sseRecord
	var dataBuf []byte
	dataLen := 0
	sawField := false
	for {
		line, err := readSSELineBounded(br, maxBytes)
		if err != nil && !errors.Is(err, io.EOF) {
			return sseRecord{}, err
		}
		if len(line) > 0 {
			switch {
			case line[0] == ':':
				continue // comment line; never terminates a record
			case bytes.HasPrefix(line, []byte("event:")):
				rec.eventType = sseFieldValue(string(line[len("event:"):]))
				sawField = true
			case bytes.HasPrefix(line, []byte("data:")):
				v := sseFieldValue(string(line[len("data:"):]))
				if len(v) > 0 {
					// The newline separator only counts once there is a prior
					// data line; a single-line payload of exactly maxBytes must
					// be accepted (no off-by-one).
					separator := 0
					if dataLen > 0 {
						separator = 1
					}
					if dataLen+len(v)+separator > maxBytes {
						return sseRecord{}, fmt.Errorf("%s: %w: SSE data payload exceeds %d bytes", ID, ErrMalformedResponse, maxBytes)
					}
					if dataLen > 0 {
						dataBuf = append(dataBuf, '\n')
						dataLen++
					}
					dataBuf = append(dataBuf, v...)
					dataLen += len(v)
				}
				sawField = true
			default:
				// id:/retry:/other fields are ignored by the pinned profile.
				sawField = true
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				if sawField {
					rec.data = dataBuf
					return rec, nil
				}
				return sseRecord{}, io.EOF
			}
			return sseRecord{}, err
		}
		if len(line) == 0 && sawField {
			rec.data = dataBuf
			return rec, nil
		}
	}
}

// readSSELineBounded reads one logical line (terminated by LF, CRLF, or CR)
// bounded to maxBytes bytes. It never returns more than maxBytes bytes per line. The
// returned slice excludes the trailing line terminator, which does not count
// toward the bound. err is io.EOF only when the underlying reader ends without
// a final terminator.
func readSSELineBounded(br *bufio.Reader, maxBytes int) ([]byte, error) {
	var out []byte
	for {
		frag, err := br.ReadSlice('\n')
		if len(frag) > 0 {
			out = append(out, frag...)
			// The length bound applies to the logical line content; the framing
			// newline/CR is stripped before the check so a line of exactly maxBytes
			// bytes is accepted (no off-by-one).
			if len(bytes.TrimRight(out, "\r\n")) > maxBytes {
				return nil, fmt.Errorf("%s: %w: SSE field line exceeds %d bytes", ID, ErrMalformedResponse, maxBytes)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		out = bytes.TrimRight(out, "\r\n")
		return out, err
	}
}

// sseFieldValue applies the SSE field-value rule: a single optional leading
// space after the colon is removed; everything else is preserved verbatim.
func sseFieldValue(v string) string {
	if strings.HasPrefix(v, " ") {
		return v[1:]
	}
	return v
}
