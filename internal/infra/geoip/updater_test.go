package geoip

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	coregeoip "github.com/aiproxer/aiproxer/internal/core/geoip"
)

func TestManagedUpdaterClosesUnchangedResponseBody(t *testing.T) {
	t.Parallel()

	var closes atomic.Int32

	// Redirect the request through a RoundTripper so the test does not need to
	// depend on the public DB-IP endpoint while still exercising body ownership.
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNotModified, Body: countingBody{Reader: strings.NewReader(""), closes: &closes}}, nil
	})}
	svc := New(nil)
	updater, err := NewManagedUpdater(svc, ManagedConfig{Directory: t.TempDir(), HTTPClient: client, Interval: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	result, err := updater.Update(context.Background())
	if err != nil || result != UpdateUnchanged {
		t.Fatalf("Update = %q, %v; want unchanged", result, err)
	}
	if closes.Load() != 1 {
		t.Fatalf("body closes = %d, want 1", closes.Load())
	}
}

func TestManagedUpdaterUsesConditionalRequestForActiveVersion(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	checksum := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	name := dbIPEdition + "." + checksum + ".mmdb"
	if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeManifest(dir, manifest{Version: checksum, Edition: dbIPEdition, Checksum: checksum, Path: name}); err != nil {
		t.Fatal(err)
	}
	var conditional string
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		conditional = req.Header.Get("If-Modified-Since")
		return &http.Response{StatusCode: http.StatusNotModified, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	updater, err := NewManagedUpdater(New(nil), ManagedConfig{Directory: dir, HTTPClient: client, Interval: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	result, err := updater.Update(context.Background())
	if err != nil || result != UpdateUnchanged {
		t.Fatalf("Update = %q, %v; want unchanged", result, err)
	}
	if conditional == "" {
		t.Fatal("conditional request did not include If-Modified-Since")
	}
}

func TestManagedUpdaterRejectsOversizedGzipCandidate(t *testing.T) {
	t.Parallel()

	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		pr, pw := io.Pipe()
		go func() {
			gz := gzip.NewWriter(pw)
			_, _ = io.Copy(gz, &repeatedByteReader{remaining: int64(coregeoip.MaxDatabaseDownloadBytes) + 1})
			_ = gz.Close()
			_ = pw.Close()
		}()
		return &http.Response{StatusCode: http.StatusOK, Body: pr}, nil
	})}
	svc := New(nil)
	updater, err := NewManagedUpdater(svc, ManagedConfig{Directory: t.TempDir(), HTTPClient: client, Interval: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := updater.Update(context.Background()); err == nil {
		t.Fatal("Update succeeded for oversized candidate")
	}
}

type repeatedByteReader struct {
	remaining int64
}

func (r *repeatedByteReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := len(p)
	if int64(n) > r.remaining {
		n = int(r.remaining)
	}
	for i := 0; i < n; i++ {
		p[i] = 'x'
	}
	r.remaining -= int64(n)
	return n, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type countingBody struct {
	io.Reader
	closes *atomic.Int32
}

func (b countingBody) Close() error {
	if b.closes == nil {
		return fmt.Errorf("nil close counter")
	}
	b.closes.Add(1)
	return nil
}
