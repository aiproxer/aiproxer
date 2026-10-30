package openresponsescompat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type countingResponseBody struct {
	io.Reader
	closes int
}

func (b *countingResponseBody) Close() error {
	b.closes++
	return nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestDoNonStreaming_ClosesResponseBodyExactlyOnce(t *testing.T) {
	for _, tc := range []struct {
		name        string
		contentType string
		wantErr     bool
	}{
		{name: "success", contentType: "application/json", wantErr: false},
		{name: "wrong_content_type", contentType: "text/plain", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &countingResponseBody{Reader: strings.NewReader(`{"ok":true}`)}
			hc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{tc.contentType}},
					Body:       body,
				}, nil
			})}
			_, err := doNonStreaming(context.Background(), hc, "https://provider.example/responses", []byte(`{}`), "", 1024)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr=%v", err, tc.wantErr)
			}
			if body.closes != 1 {
				t.Fatalf("response body close count = %d, want exactly 1", body.closes)
			}
		})
	}
}

func TestSanitizeHTTPTransportError_RemovesQuerySecrets(t *testing.T) {
	const secret = "sk-query-secret"
	original := &url.Error{
		Op:  "Post",
		URL: "https://provider.example/v1/responses?api_key=" + secret + "&token=also-secret",
		Err: errors.New("connection refused"),
	}

	got := sanitizeHTTPTransportError(fmt.Errorf("wrapped transport failure: %w", original))
	var sanitized *url.Error
	if !errors.As(got, &sanitized) {
		t.Fatalf("sanitized error = %T %v, want *url.Error", got, got)
	}
	if sanitized.URL != "https://provider.example/v1/responses" {
		t.Fatalf("sanitized URL = %q, want query-free endpoint", sanitized.URL)
	}
	if strings.Contains(got.Error(), secret) || strings.Contains(got.Error(), "also-secret") {
		t.Fatalf("sanitized error leaked query credentials: %v", got)
	}
}
