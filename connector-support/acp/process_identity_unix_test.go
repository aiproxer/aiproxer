//go:build !windows

package acp

import (
	"math"
	"testing"
	"time"
)

func TestParseUint(t *testing.T) {
	t.Parallel()
	cases := []struct {
		input string
		want  uint64
		ok    bool
	}{
		{"42", 42, true},
		{"0", 0, true},
		{"", 0, false},
		{"  123  ", 123, true},
		{"abc", 0, false},
		{"-1", 0, false},
	}
	for _, c := range cases {
		got, ok := parseUint(c.input)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parseUint(%q) = (%d, %v), want (%d, %v)", c.input, got, ok, c.want, c.ok)
		}
	}
}

func TestUnixSecsToTime(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		secs uint64
		want time.Time
	}{
		{name: "zero", secs: 0, want: time.Unix(0, 0)},
		{name: "typical", secs: 1_700_000_000, want: time.Unix(1_700_000_000, 0)},
		{name: "maxInt64", secs: uint64(math.MaxInt64), want: time.Unix(math.MaxInt64, 0)},
		{name: "maxInt64PlusOne", secs: uint64(math.MaxInt64) + 1, want: time.Time{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := unixSecsToTime(c.secs)
			if !got.Equal(c.want) {
				t.Fatalf("unixSecsToTime(%d) = %v, want %v", c.secs, got, c.want)
			}
		})
	}
}
