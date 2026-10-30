package toolnamecorrection_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

func TestFinalizer_MessageSanitizesHostileNames(t *testing.T) {
	t.Parallel()
	fin := defaultFinalizer(t)
	catalog := []aipapi.ToolDef{{Name: "read"}}
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "plain", raw: "nope", want: "nope"},
		{name: "comma kept", raw: "a,b", want: "a,b"},
		{name: "double quote dropped", raw: `no"pe`, want: "nope"},
		{name: "backslash dropped", raw: `no\pe`, want: "nope"},
		{name: "control bytes dropped", raw: "no\npe\t\x00", want: "nope"},
		{name: "non-ascii dropped", raw: "caf\u00e9\U0001F642", want: "caf"},
		{name: "truncated to 64 characters", raw: strings.Repeat("x", 70), want: strings.Repeat("x", 64)},
		{name: "everything dropped", raw: "\x01\"\\\u00e9", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			want := messageHeadOpen + tc.want + messageHeadClose + `"read"` + messageTail
			if got := steerInstruction(t, fin, tc.raw, catalog); got != want {
				t.Fatalf("instruction=%q\nwant         =%q", got, want)
			}
		})
	}
}

func TestFinalizer_MessageTruncation(t *testing.T) {
	t.Parallel()
	fin := defaultFinalizer(t)

	t.Run("a catalogue that fits exactly is listed in full", func(t *testing.T) {
		t.Parallel()
		// Catalog names list as JSON string literals, so a 64-byte name is a
		// 66-byte entry: 58 entries plus a 39-byte entry fill 4096 exactly.
		catalog := make([]aipapi.ToolDef, 0, 59)
		for i := range 58 {
			catalog = append(catalog, aipapi.ToolDef{Name: fixedName(i, 64)})
		}
		catalog = append(catalog, aipapi.ToolDef{Name: fixedName(58, 37)})
		msg := steerInstruction(t, fin, "nope", catalog)
		if len(msg) != toolcall.MaxSteerInstructionBytes {
			t.Fatalf("len(instruction)=%d, want exactly %d", len(msg), toolcall.MaxSteerInstructionBytes)
		}
		if strings.Contains(msg, " more") {
			t.Fatalf("a catalogue that fits exactly must not be truncated: %q", msg)
		}
	})

	t.Run("one byte over truncates with the omitted count", func(t *testing.T) {
		t.Parallel()
		catalog := make([]aipapi.ToolDef, 0, 59)
		for i := range 58 {
			catalog = append(catalog, aipapi.ToolDef{Name: fixedName(i, 64)})
		}
		catalog = append(catalog, aipapi.ToolDef{Name: fixedName(58, 38)})
		msg := steerInstruction(t, fin, "nope", catalog)
		if len(msg) > toolcall.MaxSteerInstructionBytes {
			t.Fatalf("len(instruction)=%d, over the %d-byte bound", len(msg), toolcall.MaxSteerInstructionBytes)
		}
		if !strings.Contains(msg, " … and 1 more") {
			t.Fatalf("truncated instruction lacks the omitted count: %q", msg)
		}
	})

	t.Run("a truncated message lands exactly on the byte bound", func(t *testing.T) {
		t.Parallel()
		// 58 entries of 66 bytes plus a 23-byte entry fill the truncation
		// budget exactly; with 10 further names omitted the suffix lands on 4096.
		listed := make([]string, 0, 59)
		catalog := make([]aipapi.ToolDef, 0, 69)
		for i := range 58 {
			name := fixedName(i, 64)
			listed = append(listed, `"`+name+`"`)
			catalog = append(catalog, aipapi.ToolDef{Name: name})
		}
		last := fixedName(58, 21)
		listed = append(listed, `"`+last+`"`)
		catalog = append(catalog, aipapi.ToolDef{Name: last})
		for i := 59; i < 69; i++ {
			catalog = append(catalog, aipapi.ToolDef{Name: fixedName(i, 64)})
		}
		msg := steerInstruction(t, fin, "nope", catalog)
		want := messageHeadOpen + "nope" + messageHeadClose +
			strings.Join(listed, ", ") + " … and 10 more" + messageTail
		if msg != want {
			t.Fatalf("instruction=%q\nwant         =%q", msg, want)
		}
		if len(msg) != toolcall.MaxSteerInstructionBytes {
			t.Fatalf("len(instruction)=%d, want exactly %d", len(msg), toolcall.MaxSteerInstructionBytes)
		}
	})

	t.Run("a name that only fits without the count is omitted", func(t *testing.T) {
		t.Parallel()
		// The 59-name catalogue above fits exactly without a suffix. One more
		// name forces truncation, and the held-back suffix room omits the
		// 39-byte tail that fit before.
		catalog := make([]aipapi.ToolDef, 0, 60)
		for i := range 58 {
			catalog = append(catalog, aipapi.ToolDef{Name: fixedName(i, 64)})
		}
		tail := fixedName(58, 37)
		catalog = append(catalog, aipapi.ToolDef{Name: tail})
		catalog = append(catalog, aipapi.ToolDef{Name: fixedName(59, 15)})
		msg := steerInstruction(t, fin, "nope", catalog)
		if strings.Contains(msg, `"`+tail+`"`) {
			t.Fatalf("tail name %q fits without the count but must be omitted: %q", tail, msg)
		}
		if !strings.Contains(msg, " … and 2 more") {
			t.Fatalf("truncated instruction lacks the omitted count: %q", msg)
		}
		if len(msg) > toolcall.MaxSteerInstructionBytes {
			t.Fatalf("len(instruction)=%d, over the %d-byte bound", len(msg), toolcall.MaxSteerInstructionBytes)
		}
	})

	t.Run("escaping overhead counts against the byte budget", func(t *testing.T) {
		t.Parallel()
		// 60 names of 32 quotes are 2151 bytes raw but 4191 encoded, so the
		// encoded measure truncates where the raw measure would fit.
		catalog := make([]aipapi.ToolDef, 0, 60)
		for range 60 {
			catalog = append(catalog, aipapi.ToolDef{Name: strings.Repeat(`"`, 32)})
		}
		msg := steerInstruction(t, fin, "nope", catalog)
		if len(msg) > toolcall.MaxSteerInstructionBytes {
			t.Fatalf("len(instruction)=%d, over the %d-byte bound", len(msg), toolcall.MaxSteerInstructionBytes)
		}
		if !strings.Contains(msg, " more") {
			t.Fatalf("encoded entries overflow the budget and must truncate: %q", msg)
		}
	})

	t.Run("catalogue order is kept and the count matches the omissions", func(t *testing.T) {
		t.Parallel()
		names := make([]string, 70)
		catalog := make([]aipapi.ToolDef, len(names))
		for i := range names {
			names[i] = fixedName(i, 64)
			catalog[i] = aipapi.ToolDef{Name: names[i]}
		}
		msg := steerInstruction(t, fin, "nope", catalog)
		quoted := make([]string, 58)
		for i, name := range names[:58] {
			quoted[i] = `"` + name + `"`
		}
		want := messageHeadOpen + "nope" + messageHeadClose +
			strings.Join(quoted, ", ") + " … and 12 more" + messageTail
		if msg != want {
			t.Fatalf("instruction=%q\nwant         =%q", msg, want)
		}
		for _, omitted := range names[58:] {
			if strings.Contains(msg, `"`+omitted+`"`) {
				t.Fatalf("omitted name %q must not be listed: %q", omitted, msg)
			}
		}
	})
}

// fixedName returns a distinct catalog name of exactly size bytes. The index
// tag leads so names stay distinct.
func fixedName(index, size int) string {
	tag := fmt.Sprintf("t%02d-", index)
	return tag + strings.Repeat("x", size-len(tag))
}

func TestFinalizer_MessageListsCatalogNamesExactly(t *testing.T) {
	t.Parallel()
	fin := defaultFinalizer(t)
	long := strings.Repeat("x", 70)
	cases := []struct {
		name        string
		invalid     string
		wantInvalid string
		raw         string
		want        string
	}{
		{name: "70-character name kept in full", invalid: "nope", wantInvalid: "nope", raw: long, want: `"` + long + `"`},
		{name: "comma kept", invalid: "nope", wantInvalid: "nope", raw: "a,b", want: `"a,b"`},
		{name: "double quote escaped", invalid: "nope", wantInvalid: "nope", raw: `re"ad`, want: `"re\"ad"`},
		{name: "newline escaped", invalid: "nope", wantInvalid: "nope", raw: "re\nad", want: `"re\nad"`},
		{name: "control byte escaped", invalid: "nope", wantInvalid: "nope", raw: "re\x01ad", want: `"re\u0001ad"`},
		{name: "empty name listed as empty string", invalid: "nope", wantInvalid: "nope", raw: "", want: `""`},
		{
			name:        "hostile invented name still sanitized",
			invalid:     "no\n\"pe\\,x",
			wantInvalid: "nope,x",
			raw:         "read",
			want:        `"read"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			catalog := []aipapi.ToolDef{{Name: tc.raw}}
			want := messageHeadOpen + tc.wantInvalid + messageHeadClose + tc.want + messageTail
			if got := steerInstruction(t, fin, tc.invalid, catalog); got != want {
				t.Fatalf("instruction=%q\nwant         =%q", got, want)
			}
			var decoded []string
			if err := json.Unmarshal([]byte("["+tc.want+"]"), &decoded); err != nil ||
				len(decoded) != 1 || decoded[0] != tc.raw {
				t.Fatalf("entry %s does not decode to %q: %v", tc.want, tc.raw, err)
			}
		})
	}
}
