package repair_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair/repair"
	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair/repair/jsonshape"
)

func nestedArrayJSON(depth int) []byte {
	return []byte(strings.Repeat(`[`, depth) + `1` + strings.Repeat(`]`, depth))
}

func wideObjectJSON(keys int) []byte {
	var b strings.Builder
	b.WriteByte('{')
	for i := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"k%d":%d`, i, i)
	}
	b.WriteByte('}')
	return []byte(b.String())
}

func wideArrayJSON(n int) []byte {
	var b strings.Builder
	b.WriteByte('[')
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `%d`, i%10)
	}
	b.WriteByte(']')
	return []byte(b.String())
}

func mixedArgsNear64KiB() []byte {
	const target = 60 << 10
	chunk := strings.Repeat("x", 256)
	var b strings.Builder
	b.WriteString(`{"items":[`)
	for b.Len() < target {
		if b.Len() > len(`{"items":[`) {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"id":%d,"t":"%s"}`, b.Len(), chunk)
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

func TestOrderedParse_depth64SucceedsAfterPreflight(t *testing.T) {
	t.Parallel()
	depth := jsonshape.ToolArgumentsLimits().MaxDepth
	args := nestedArrayJSON(depth)
	if err := repair.ExportPreflightArgsJSON(context.Background(), args, repair.DefaultMaxArgsBytes); err != nil {
		t.Fatalf("preflight depth %d: %v", depth, err)
	}
	v, err := repair.ExportParseOrderedJSON(args)
	if err != nil {
		t.Fatalf("parse depth %d: %v", depth, err)
	}
	if v == nil {
		t.Fatal("nil parse result")
	}
}

func TestOrderedParse_depth65RejectedBeforeParser(t *testing.T) {
	t.Parallel()
	depth := jsonshape.ToolArgumentsLimits().MaxDepth + 1
	args := nestedArrayJSON(depth)
	err := repair.ExportPreflightArgsJSON(context.Background(), args, repair.DefaultMaxArgsBytes)
	if err == nil {
		t.Fatalf("preflight depth %d should fail", depth)
	}
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("parse panicked on depth %d: %v", depth, r)
			}
		}()
		_, _ = repair.ExportParseOrderedJSON(args)
	}()
}

func TestOrderedParse_preflightPlusParseDuplicateKeyOrderNumber(t *testing.T) {
	t.Parallel()
	dup := []byte(`{"a":1,"a":2}`)
	if err := repair.ExportPreflightArgsJSON(context.Background(), dup, repair.DefaultMaxArgsBytes); err == nil {
		t.Fatal("preflight should reject duplicate keys")
	}
	if _, err := repair.ExportParseOrderedJSON(dup); err == nil {
		t.Fatal("parse should reject duplicate keys")
	}

	num := []byte(`{"n":1.0,"k":["x","y"]}`)
	if err := repair.ExportPreflightArgsJSON(context.Background(), num, repair.DefaultMaxArgsBytes); err != nil {
		t.Fatal(err)
	}
	v, err := repair.ExportParseOrderedJSON(num)
	if err != nil {
		t.Fatal(err)
	}
	keys, values, ok := repair.ExportObjectFields(v)
	if !ok || len(keys) != 2 || keys[0] != "n" || keys[1] != "k" {
		t.Fatalf("key order lost: keys=%v ok=%v", keys, ok)
	}
	n, ok := values["n"].(json.Number)
	if !ok || string(n) != "1.0" {
		t.Fatalf("json.Number spelling lost: %#v", values["n"])
	}
}
