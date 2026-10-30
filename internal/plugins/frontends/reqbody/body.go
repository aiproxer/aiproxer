// Package reqbody centralizes bounded HTTP request body reads for frontend handlers.
package reqbody

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// DefaultMaxBytes is the maximum request body size when no explicit limit is set.
const DefaultMaxBytes int64 = 8 << 20

// ReadAll reads r.Body using http.MaxBytesReader. On limit exceeded it returns a non-nil err
// for which TooLarge returns true; callers should respond with HTTP 413 without treating it as JSON parse failure.
//
// When the request advertises Content-Encoding: gzip, the body is transparently decompressed
// and the byte limit is applied to the decompressed size (mitigating decompression bombs).
func ReadAll(w http.ResponseWriter, r *http.Request, maxBytes int64) (data []byte, err error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	src := r.Body
	var gzr *gzip.Reader
	if isGzipEncoded(r) {
		gzr, err = gzip.NewReader(src)
		if err != nil {
			if cerr := src.Close(); cerr != nil {
				err = errors.Join(err, fmt.Errorf("reqbody: close body reader: %w", cerr))
			}
			return nil, err
		}
	}
	reader := src
	if gzr != nil {
		reader = gzr
	}
	lr := http.MaxBytesReader(w, reader, maxBytes)
	defer func() {
		var cerrs []error
		if cerr := lr.Close(); cerr != nil {
			cerrs = append(cerrs, fmt.Errorf("reqbody: close body reader: %w", cerr))
		}
		// gzip.Reader.Close does not close the underlying body, so close src explicitly.
		if gzr != nil {
			if cerr := src.Close(); cerr != nil {
				cerrs = append(cerrs, fmt.Errorf("reqbody: close gzip source body: %w", cerr))
			}
		}
		if len(cerrs) > 0 {
			closeErr := errors.Join(cerrs...)
			if err != nil {
				err = errors.Join(err, closeErr)
			} else {
				err = closeErr
			}
		}
	}()
	data, err = io.ReadAll(lr)
	return data, err
}

func isGzipEncoded(r *http.Request) bool {
	for _, h := range r.Header.Values("Content-Encoding") {
		for part := range strings.SplitSeq(h, ",") {
			if strings.EqualFold(strings.TrimSpace(part), "gzip") {
				return true
			}
		}
	}
	return false
}

// TooLarge reports whether err is from exceeding MaxBytesReader's limit.
// It uses errors.As so any error in the chain that unwraps to *http.MaxBytesError matches.
func TooLarge(err error) bool {
	if err == nil {
		return false
	}
	var maxBytesErr *http.MaxBytesError
	return errors.As(err, &maxBytesErr)
}
