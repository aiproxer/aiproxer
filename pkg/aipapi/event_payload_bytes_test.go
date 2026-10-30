package aipapi_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// eventMemoryAllowList names string-typed Event leaves that PayloadBytes
// deliberately does not count: fixed small vocabularies, bounded.
var eventMemoryAllowList = map[string]string{
	"Item.Kind":                  "fixed item-kind vocabulary, bounded",
	"Item.Status":                "fixed item-status vocabulary, bounded",
	"Item.Role":                  "fixed role vocabulary, bounded",
	"Item.Phase":                 "fixed assistant-phase vocabulary, bounded",
	"Item.Content.Kind":          "fixed content-part vocabulary, bounded",
	"Item.ToolResult.Parts.Kind": "fixed content-part vocabulary, bounded",
}

// eventLeaf is one field reachable from aipapi.Event that the guards must
// cover: every string, byte-slice, slice, and pointer in the Event graph.
type eventLeaf struct {
	path []string
	name string
	typ  reflect.Type
}

// collectEventLeaves walks every field reachable from aipapi.Event. It
// follows pointers by allocating the pointee, slice elements by descending
// into the element type, and nested structs. Bool, numeric, and other
// fixed-size kinds are skipped by kind, not by name. Cycles are guarded by
// an ancestor-type set and a depth cap.
func collectEventLeaves(t *testing.T) []eventLeaf {
	t.Helper()
	const maxDepth = 20
	var out []eventLeaf
	var walkStruct func(st reflect.Type, base []string, ancestors map[reflect.Type]bool, depth int)
	walkField := func(ft reflect.Type, path []string, ancestors map[reflect.Type]bool, depth int) {
		if depth > maxDepth {
			t.Fatalf("event leaf walk depth cap exceeded at %s", strings.Join(path, "."))
		}
		name := strings.Join(path, ".")
		leaf := eventLeaf{path: append([]string(nil), path...), name: name, typ: ft}
		switch ft.Kind() {
		case reflect.String:
			out = append(out, leaf)
		case reflect.Slice:
			out = append(out, leaf)
			et := ft.Elem()
			switch et.Kind() {
			case reflect.Uint8, reflect.Float64, reflect.String:
			case reflect.Struct:
				walkStruct(et, path, ancestors, depth+1)
			case reflect.Pointer:
				if et.Elem().Kind() == reflect.Struct {
					walkStruct(et.Elem(), path, ancestors, depth+1)
				}
			default:
				t.Fatalf("slice element %s kind %s needs explicit handling", name, et.Kind())
			}
		case reflect.Pointer:
			out = append(out, leaf)
			if et := ft.Elem(); et.Kind() == reflect.Struct {
				walkStruct(et, path, ancestors, depth+1)
			}
		case reflect.Struct:
			walkStruct(ft, path, ancestors, depth+1)
		case reflect.Bool,
			reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
			reflect.Float32, reflect.Float64:
			// Skipped by kind: genuinely not memory-bearing.
		default:
			t.Fatalf("field %s kind %s needs explicit handling", name, ft.Kind())
		}
	}
	walkStruct = func(st reflect.Type, base []string, ancestors map[reflect.Type]bool, depth int) {
		if depth > maxDepth {
			t.Fatalf("event leaf walk depth cap exceeded at %s", strings.Join(base, "."))
		}
		if ancestors[st] {
			return
		}
		ancestors[st] = true
		defer delete(ancestors, st)
		for f := range st.Fields() {
			if !f.IsExported() {
				continue
			}
			path := append(append([]string(nil), base...), f.Name)
			walkField(f.Type, path, ancestors, depth+1)
		}
	}
	walkStruct(reflect.TypeFor[aipapi.Event](), nil, map[reflect.Type]bool{}, 0)
	if len(out) == 0 {
		t.Fatal("event leaf walk found no leaves")
	}
	return out
}

// allocEventPath navigates root along path, allocating intermediate pointers
// and one-element slices, and returns the leaf value.
func allocEventPath(t *testing.T, root reflect.Value, path []string) reflect.Value {
	t.Helper()
	cur := root
	for _, name := range path[:len(path)-1] {
		f := cur.FieldByName(name)
		if !f.IsValid() || !f.CanSet() {
			t.Fatalf("path %s: field %s missing or unsettable", strings.Join(path, "."), name)
		}
		switch f.Kind() {
		case reflect.Pointer:
			if f.IsNil() {
				f.Set(reflect.New(f.Type().Elem()))
			}
			cur = f.Elem()
		case reflect.Slice:
			if f.Len() == 0 {
				f.Set(reflect.MakeSlice(f.Type(), 1, 1))
			}
			elem := f.Index(0)
			if elem.Kind() == reflect.Pointer {
				if elem.IsNil() {
					elem.Set(reflect.New(elem.Type().Elem()))
				}
				cur = elem.Elem()
			} else {
				cur = elem
			}
		case reflect.Struct:
			cur = f
		default:
			t.Fatalf("path %s: intermediate %s kind %s needs explicit handling", strings.Join(path, "."), name, f.Kind())
		}
	}
	leaf := cur.FieldByName(path[len(path)-1])
	if !leaf.IsValid() || !leaf.CanSet() {
		t.Fatalf("path %s: leaf missing or unsettable", strings.Join(path, "."))
	}
	return leaf
}

// makeEventWithLeaf builds an Event with the path to leaf allocated and set
// applies only to that leaf.
func makeEventWithLeaf(t *testing.T, path []string, set func(leaf reflect.Value)) aipapi.Event {
	t.Helper()
	var ev aipapi.Event
	leaf := allocEventPath(t, reflect.ValueOf(&ev).Elem(), path)
	set(leaf)
	return ev
}

// lookupEventLeaf follows path through pointers and slice elements without
// allocating and returns the leaf value.
func lookupEventLeaf(t *testing.T, ev aipapi.Event, path []string) reflect.Value {
	t.Helper()
	cur := reflect.ValueOf(ev)
	for i, name := range path {
		for cur.Kind() == reflect.Pointer {
			if cur.IsNil() {
				t.Fatalf("path %s: nil pointer at %s", strings.Join(path, "."), strings.Join(path[:i], "."))
			}
			cur = cur.Elem()
		}
		if cur.Kind() != reflect.Struct {
			t.Fatalf("path %s: %s is %s, want struct", strings.Join(path, "."), strings.Join(path[:i], "."), cur.Kind())
		}
		f := cur.FieldByName(name)
		if !f.IsValid() {
			t.Fatalf("path %s: field %s missing", strings.Join(path, "."), name)
		}
		if i == len(path)-1 {
			return f
		}
		switch f.Kind() {
		case reflect.Pointer:
			if f.IsNil() {
				t.Fatalf("path %s: nil pointer at %s", strings.Join(path, "."), strings.Join(path[:i+1], "."))
			}
			cur = f.Elem()
		case reflect.Slice:
			if f.Len() == 0 {
				t.Fatalf("path %s: empty slice at %s", strings.Join(path, "."), strings.Join(path[:i+1], "."))
			}
			elem := f.Index(0)
			if elem.Kind() == reflect.Pointer {
				if elem.IsNil() {
					t.Fatalf("path %s: nil element at %s", strings.Join(path, "."), strings.Join(path[:i+1], "."))
				}
				cur = elem.Elem()
			} else {
				cur = elem
			}
		case reflect.Struct:
			cur = f
		default:
			t.Fatalf("path %s: intermediate %s kind %s needs explicit handling", strings.Join(path, "."), name, f.Kind())
		}
	}
	t.Fatalf("path %s: empty path", strings.Join(path, "."))
	return reflect.Value{}
}

// checkEventPayloadBytes proves PayloadBytes grows with every memory-bearing
// leaf: N bytes of string or byte content grow by at least N against the same
// path with that leaf empty, N zero slice elements grow by at least N element
// sizes, and a non-nil pointer grows by at least its pointee size.
func checkEventPayloadBytes(t *testing.T, leaves []eventLeaf) {
	t.Helper()
	const big = 4096
	const structuralN = 64
	seenAllow := map[string]bool{}
	for _, lf := range leaves {
		switch {
		case lf.typ.Kind() == reflect.String:
			if _, ok := eventMemoryAllowList[lf.name]; ok {
				seenAllow[lf.name] = true
				continue
			}
			baseEv := makeEventWithLeaf(t, lf.path, func(v reflect.Value) {})
			filledEv := makeEventWithLeaf(t, lf.path, func(v reflect.Value) {
				v.SetString(strings.Repeat("x", big))
			})
			if got := filledEv.PayloadBytes() - baseEv.PayloadBytes(); got < big {
				t.Errorf("%s: PayloadBytes grew by %d, want at least %d", lf.name, got, big)
			}
		case lf.typ.Kind() == reflect.Slice && lf.typ.Elem().Kind() == reflect.Uint8:
			baseEv := makeEventWithLeaf(t, lf.path, func(v reflect.Value) {})
			filledEv := makeEventWithLeaf(t, lf.path, func(v reflect.Value) {
				v.Set(reflect.MakeSlice(v.Type(), big, big))
			})
			if got := filledEv.PayloadBytes() - baseEv.PayloadBytes(); got < big {
				t.Errorf("%s: PayloadBytes grew by %d, want at least %d", lf.name, got, big)
			}
		case lf.typ.Kind() == reflect.Slice:
			baseEv := makeEventWithLeaf(t, lf.path, func(v reflect.Value) {})
			filledEv := makeEventWithLeaf(t, lf.path, func(v reflect.Value) {
				v.Set(reflect.MakeSlice(v.Type(), structuralN, structuralN))
			})
			if want := structuralN * int(lf.typ.Elem().Size()); filledEv.PayloadBytes()-baseEv.PayloadBytes() < want {
				t.Errorf("%s: PayloadBytes grew by %d, want at least %d for %d zero elements",
					lf.name, filledEv.PayloadBytes()-baseEv.PayloadBytes(), want, structuralN)
			}
		case lf.typ.Kind() == reflect.Pointer:
			baseEv := makeEventWithLeaf(t, lf.path, func(v reflect.Value) {})
			filledEv := makeEventWithLeaf(t, lf.path, func(v reflect.Value) {
				v.Set(reflect.New(v.Type().Elem()))
			})
			if want := int(lf.typ.Elem().Size()); filledEv.PayloadBytes()-baseEv.PayloadBytes() < want {
				t.Errorf("%s: PayloadBytes grew by %d, want at least %d for non-nil pointer",
					lf.name, filledEv.PayloadBytes()-baseEv.PayloadBytes(), want)
			}
		default:
			t.Fatalf("leaf %s kind %s needs explicit handling", lf.name, lf.typ.Kind())
		}
	}
	for name := range eventMemoryAllowList {
		if !seenAllow[name] {
			t.Errorf("allow-list entry %s matches no Event leaf: remove it", name)
		}
	}
	// UsagePresence records explicit zero counters and must never affect the
	// memory bound.
	base := aipapi.Event{}.PayloadBytes()
	full := aipapi.Event{UsagePresence: aipapi.UsagePresence{
		InputTokens: true, OutputTokens: true, CacheReadTokens: true,
		CacheWriteTokens: true, ReasoningTokens: true, TotalTokens: true,
	}}
	if got := full.PayloadBytes(); got != base {
		t.Errorf("UsagePresence must not affect PayloadBytes, got %d want %d", got, base)
	}
}

// checkEventClone proves Event.Clone detaches every slice and pointer leaf:
// a non-empty slice and an empty slice with spare capacity must not share
// the original backing pointer (a nil clone is acceptable for the empty
// case only), and every pointer must hold a different address.
func checkEventClone(t *testing.T, leaves []eventLeaf) {
	t.Helper()
	for _, lf := range leaves {
		switch lf.typ.Kind() {
		case reflect.Slice:
			orig := makeEventWithLeaf(t, lf.path, func(v reflect.Value) {
				if v.Type().Elem().Kind() == reflect.Uint8 {
					v.Set(reflect.MakeSlice(v.Type(), 16, 16))
				} else {
					v.Set(reflect.MakeSlice(v.Type(), 2, 2))
				}
			})
			cl := orig.Clone()
			origLeaf := lookupEventLeaf(t, orig, lf.path)
			cloneLeaf := lookupEventLeaf(t, cl, lf.path)
			if cloneLeaf.IsNil() {
				t.Errorf("%s: clone lost non-empty slice (nil)", lf.name)
				continue
			}
			if origLeaf.Pointer() == cloneLeaf.Pointer() {
				t.Errorf("%s: clone shares the original backing array", lf.name)
			}
			origEmpty := makeEventWithLeaf(t, lf.path, func(v reflect.Value) {
				v.Set(reflect.MakeSlice(v.Type(), 0, 64))
			})
			clEmpty := origEmpty.Clone()
			origEmptyLeaf := lookupEventLeaf(t, origEmpty, lf.path)
			cloneEmptyLeaf := lookupEventLeaf(t, clEmpty, lf.path)
			if !cloneEmptyLeaf.IsNil() && origEmptyLeaf.Pointer() == cloneEmptyLeaf.Pointer() {
				t.Errorf("%s: empty clone shares the original backing array", lf.name)
			}
		case reflect.Pointer:
			orig := makeEventWithLeaf(t, lf.path, func(v reflect.Value) {
				v.Set(reflect.New(v.Type().Elem()))
			})
			cl := orig.Clone()
			origLeaf := lookupEventLeaf(t, orig, lf.path)
			cloneLeaf := lookupEventLeaf(t, cl, lf.path)
			if cloneLeaf.IsNil() {
				t.Errorf("%s: clone lost pointer (nil)", lf.name)
				continue
			}
			if origLeaf.Pointer() == cloneLeaf.Pointer() {
				t.Errorf("%s: clone shares the original pointer", lf.name)
			}
		case reflect.String:
			// Strings are immutable; no aliasing to prove.
		default:
			t.Fatalf("leaf %s kind %s needs explicit handling", lf.name, lf.typ.Kind())
		}
	}
}

// TestEventPayloadBytesCoversEveryMemoryBearingField guards the withhold
// memory bound with one recursive reflection walker: PayloadBytes must grow
// with every string, byte-slice, slice, and pointer reachable from Event. A
// new memory-bearing Event field fails here until PayloadBytes counts it.
func TestEventPayloadBytesCoversEveryMemoryBearingField(t *testing.T) {
	t.Parallel()
	checkEventPayloadBytes(t, collectEventLeaves(t))
}

// TestEventClone_DetachesDecisionResult proves the withhold queue's retained
// copy owns its decision result: the answers slice, every probability backing
// array and every confidence value are detached, so a producer that reuses its
// own storage cannot change what the queue replays.
func TestEventClone_DetachesDecisionResult(t *testing.T) {
	t.Parallel()

	confidence := 0.75
	ev := aipapi.Event{Kind: aipapi.EventDecisionResult, Decision: &aipapi.DecisionResult{
		Model: "jev-latest",
		Answers: []aipapi.DecisionAnswer{
			{QuestionID: "is-ok", Kind: aipapi.DecisionKindNoul, PTrue: 0.9},
			{QuestionID: "which-region", Kind: aipapi.DecisionKindChoice, Selected: "us", Probabilities: []float64{0.25, 0.75}, Confidence: &confidence},
		},
	}}
	cl := ev.Clone()
	if cl.Decision == ev.Decision {
		t.Fatal("Event.Clone shares the decision result pointer")
	}
	if len(cl.Decision.Answers) != len(ev.Decision.Answers) {
		t.Fatalf("Event.Clone answers = %d, want %d", len(cl.Decision.Answers), len(ev.Decision.Answers))
	}
	if &cl.Decision.Answers[0] == &ev.Decision.Answers[0] {
		t.Fatal("Event.Clone shares the decision answers backing array")
	}
	// Reusing the producer's own storage must not reach the clone.
	ev.Decision.Model = "mutated"
	ev.Decision.Answers[0].QuestionID = "mutated"
	ev.Decision.Answers[1].Probabilities[0] = 1
	*ev.Decision.Answers[1].Confidence = 1
	if cl.Decision.Model != "jev-latest" ||
		cl.Decision.Answers[0].QuestionID != "is-ok" ||
		cl.Decision.Answers[1].Probabilities[0] != 0.25 ||
		*cl.Decision.Answers[1].Confidence != 0.75 {
		t.Fatalf("producer mutation reached the clone: %+v", cl.Decision)
	}
}
