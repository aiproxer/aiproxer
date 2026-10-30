package ssestream_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/ssestream"
)

type closeCounter struct {
	io.Reader
	closes int
}

func (c *closeCounter) Close() error { c.closes++; return nil }

func textDecoder() ssestream.Decoder {
	return ssestream.Decoder{
		Start: func() []aipapi.Event { return []aipapi.Event{{Kind: aipapi.EventResponseStarted}} },
		Data: func(p []byte) ([]aipapi.Event, error) {
			if string(p) == "bad" {
				return nil, errors.New("decode failed")
			}
			if string(p) == "skip" {
				return nil, nil
			}
			return []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: string(p)}}, nil
		},
		End: func() []aipapi.Event { return []aipapi.Event{{Kind: aipapi.EventResponseFinished}} },
	}
}

func drain(t *testing.T, s *ssestream.Stream) ([]aipapi.Event, error) {
	t.Helper()
	var out []aipapi.Event
	for range 100 {
		ev, err := s.Recv(context.Background())
		if err != nil {
			return out, err
		}
		out = append(out, ev)
	}
	t.Fatal("stream did not terminate")
	return nil, nil
}

func kinds(evs []aipapi.Event) string {
	parts := make([]string, 0, len(evs))
	for _, ev := range evs {
		p := string(ev.Kind)
		if ev.Delta != "" {
			p += "(" + ev.Delta + ")"
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, " ")
}

func TestStream_Framing(t *testing.T) {
	t.Parallel()
	started, finished := string(aipapi.EventResponseStarted), string(aipapi.EventResponseFinished)
	text := string(aipapi.EventTextDelta)
	cases := []struct {
		name string
		body string
		want string
	}{
		{"empty body still starts and ends", "", started + " " + finished},
		{"data lines, comments and fields", ": ping\nevent: delta\ndata: a\n\nid: 7\ndata:b\n", started + " " + text + "(a) " + text + "(b) " + finished},
		{"blank data and skipped payload", "data:\ndata: skip\ndata: c\n", started + " " + text + "(c) " + finished},
		{"[DONE] ends once and ignores the rest", "data: a\ndata: [DONE]\ndata: late\n", started + " " + text + "(a) " + finished},
		{"surrounding whitespace", "  data:  d  \r\n", started + " " + text + "(d) " + finished},
	}
	for _, tc := range cases {
		s := ssestream.New(io.NopCloser(strings.NewReader(tc.body)), textDecoder(), ssestream.Options{Name: "p"})
		evs, err := drain(t, s)
		if !errors.Is(err, io.EOF) {
			t.Errorf("%s: terminal err = %v, want io.EOF", tc.name, err)
		}
		if got := kinds(evs); got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, got, tc.want)
		}
	}
}

func TestStream_Failures(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
		opts ssestream.Options
		want string
	}{
		{"byte cap", "data: aaaaaaaaaa\ndata: bbbbbbbbbb\n", ssestream.Options{Name: "p", MaxBytes: 16}, "p: sse exceeded 16 bytes"},
		{"decoder error", "data: bad\n", ssestream.Options{Name: "p"}, "decode failed"},
		{"line over buffer", "data: " + strings.Repeat("x", 1024*1024+1) + "\n", ssestream.Options{Name: "p"}, "p: sse: bufio.Scanner: token too long"},
	}
	for _, tc := range cases {
		dec := textDecoder()
		dec.End = func() []aipapi.Event { t.Errorf("%s: End ran after a failure", tc.name); return nil }
		s := ssestream.New(io.NopCloser(strings.NewReader(tc.body)), dec, tc.opts)
		_, err := drain(t, s)
		if err == nil || errors.Is(err, io.EOF) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want containing %q", tc.name, err, tc.want)
		}
	}
}

func TestStream_ProjectCancelAndClose(t *testing.T) {
	t.Parallel()
	body := &closeCounter{Reader: strings.NewReader("data: a\n")}
	projected := 0
	s := ssestream.New(body, textDecoder(), ssestream.Options{Name: "p", Project: func(ev aipapi.Event) aipapi.Event {
		projected++
		return ev
	}})

	ctx, cancel := context.WithCancel(context.Background())
	if _, err := s.Recv(ctx); err != nil {
		t.Fatalf("first Recv: %v", err)
	}
	cancel()
	if _, err := s.Recv(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Recv after cancel = %v, want context.Canceled", err)
	}
	if projected != 1 {
		t.Fatalf("projected %d events, want 1", projected)
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if body.closes != 1 {
		t.Fatalf("body closed %d times, want 1", body.closes)
	}
	if _, err := s.Recv(context.Background()); err == nil || !strings.Contains(err.Error(), "p: stream closed") {
		t.Fatalf("Recv after Close = %v, want stream closed", err)
	}
}
