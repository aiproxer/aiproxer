package toolcall_test

import (
	"reflect"
	"testing"

	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// Spec: b-leg-path-virtualization Task 6.1. Requirements 4.1, 5.1, 6.1 and
// design.md "5. Completed Tool-Call Finalizer Metadata".
//
// The runtime populates toolcall.Meta scope/session/workspace from the same
// authoritative request views it already hands tool policy and tool reactor
// metadata. This characterization pins the shape additively: the four
// pre-existing identity fields keep their index, type, and name, and the new
// views are the exact types hooks.ToolMeta already uses.
func TestMeta_ViewFieldSetMatchesToolMetaAuthoritativeViewSemantics(t *testing.T) {
	t.Parallel()

	metaType := reflect.TypeFor[toolcall.Meta]()
	hookType := reflect.TypeFor[sdkhooks.ToolMeta]()

	// Legacy identity fields are append-only frozen: same name, same index,
	// same type. Anything else is a breaking change for a public SDK struct.
	wantKind := map[string]reflect.Kind{
		"TraceID":    reflect.String,
		"ALegID":     reflect.String,
		"BLegID":     reflect.String,
		"AttemptSeq": reflect.Int,
	}
	for index, name := range []string{"TraceID", "ALegID", "BLegID", "AttemptSeq"} {
		field, ok := metaType.FieldByName(name)
		if !ok {
			t.Errorf("Meta must retain pre-existing field %q", name)
			continue
		}
		if field.Index[0] != index {
			t.Errorf("Meta field %q index = %d, want %d (pre-existing field order is frozen)", name, field.Index[0], index)
		}
		if got := field.Type.Kind(); got != wantKind[name] {
			t.Errorf("Meta field %q kind = %v, want %v", name, got, wantKind[name])
		}
	}

	for _, name := range []string{"Scope", "Session", "Workspace"} {
		field, ok := metaType.FieldByName(name)
		if !ok {
			t.Errorf("RED: design.md \"Completed Tool-Call Finalizer Metadata\" - Meta must expose the authoritative %s view so a finalizer reads the same snapshot as tool policy/reactor metadata", name)
			continue
		}
		hookField, hookOK := hookType.FieldByName(name)
		if !hookOK {
			t.Errorf("hooks.ToolMeta must expose %q; toolcall.Meta cannot mirror a view that no longer exists", name)
			continue
		}
		if field.Type != hookField.Type {
			t.Errorf("Meta field %q type = %v, want %v (must match hooks.ToolMeta exactly)", name, field.Type, hookField.Type)
		}
		if got := field.Type.Kind(); got != reflect.Struct {
			t.Errorf("Meta field %q kind = %v, want %v: a value view keeps the zero value meaningful and cannot be nil", name, got, reflect.Struct)
		}
	}

	// design.md specifies "Extend generic toolcall.Meta additively", so the
	// count is a lower bound: a later legitimate additive field must not fail
	// this. Field identity, order and type are pinned individually above and
	// below, which is where a breaking change would actually show up.
	if got, want := metaType.NumField(), 7; got < want {
		t.Errorf("Meta field count = %d, want at least %d (the change is purely additive)", got, want)
	}

	// The three views must be appended in design order so any future field
	// addition cannot silently reshuffle them.
	prev := -1
	for _, name := range []string{"Scope", "Session", "Workspace"} {
		field, ok := metaType.FieldByName(name)
		if !ok {
			continue
		}
		if field.Index[0] <= prev {
			t.Errorf("Meta field %q index = %d, want greater than %d (design order Scope, Session, Workspace)", name, field.Index[0], prev)
		}
		prev = field.Index[0]
	}
}

// TestMeta_ViewFieldsAreInterchangeableWithToolMetaViews is the compile-time
// half of the semantic mirror: assignment both ways only type-checks when
// toolcall.Meta and hooks.ToolMeta use the identical view types, which is a
// stronger guarantee than matching field names.
func TestMeta_ViewFieldsAreInterchangeableWithToolMetaViews(t *testing.T) {
	t.Parallel()

	var meta toolcall.Meta
	var hook sdkhooks.ToolMeta

	meta.Scope = hook.Scope
	hook.Scope = meta.Scope
	meta.Session = hook.Session
	hook.Session = meta.Session
	meta.Workspace = hook.Workspace
	hook.Workspace = meta.Workspace

	// A view written on the finalization plane must read back identically on
	// the reactor plane: one authoritative snapshot, two entry points.
	wantScope := scope.PrincipalScopeView{
		SubjectKind:   scope.SubjectService,
		PrincipalID:   scope.Known("synthetic-principal"),
		Origin:        scope.OriginInternal,
		ParentTraceID: scope.Known("synthetic-parent"),
		Roles:         []string{"synthetic-role"},
		SafeClaims:    map[string]string{"synthetic_claim": "synthetic-value"},
		PolicyLabels:  map[string]string{"synthetic_label": "synthetic-value"},
	}
	meta.Scope = wantScope
	hook.Scope = meta.Scope
	if !reflect.DeepEqual(hook.Scope, wantScope) {
		t.Error("Scope view must cross between toolcall.Meta and hooks.ToolMeta unchanged")
	}

	wantSession := session.SessionView{
		AuthoritativeSessionID: "synthetic-session",
		ALegID:                 "synthetic-a-leg",
		WorkspaceID:            "synthetic-workspace-id",
		IsNew:                  true,
		Labels:                 map[string]string{"synthetic_label": "synthetic-value"},
	}
	meta.Session = wantSession
	hook.Session = meta.Session
	if !reflect.DeepEqual(hook.Session, wantSession) {
		t.Error("Session view must cross between toolcall.Meta and hooks.ToolMeta unchanged")
	}

	wantWorkspace := workspace.WorkspaceView{
		ID:          "synthetic-workspace-id",
		ProjectRoot: "synthetic-project-root",
		DirtyTree:   true,
		Markers:     []string{"synthetic-marker"},
		Labels:      map[string]string{"synthetic_label": "synthetic-value"},
	}
	meta.Workspace = wantWorkspace
	hook.Workspace = meta.Workspace
	if !reflect.DeepEqual(hook.Workspace, wantWorkspace) {
		t.Error("Workspace view must cross between toolcall.Meta and hooks.ToolMeta unchanged")
	}
}

// TestMeta_ZeroViewsPreserveLegacyIdentitySemantics proves the zero value of
// the additive fields carries the same meaning hooks.ToolMeta already gives
// it: absent, not broken. A finalizer that ignores the new fields must behave
// exactly as it did before the extension.
func TestMeta_ZeroViewsPreserveLegacyIdentitySemantics(t *testing.T) {
	t.Parallel()

	meta := toolcall.Meta{}
	if got := meta.Scope.SubjectKind; got != "" {
		t.Errorf("zero Scope subject kind = %q, want the empty zero value", got)
	}
	if got := meta.Scope.PrincipalID; !got.IsUnknown() {
		t.Error("zero Scope principal id must be unknown, not present")
	}
	if got := meta.Scope.Origin; got != "" {
		t.Errorf("zero Scope origin = %q, want the empty zero value", got)
	}
	if meta.Scope.Roles != nil || meta.Scope.SafeClaims != nil || meta.Scope.PolicyLabels != nil {
		t.Error("zero Scope must not fabricate reference-typed state")
	}
	if got := meta.Session.PartitionKey(); got != "" {
		t.Errorf("zero Session partition key = %q, want empty", got)
	}
	if meta.Session.Labels != nil {
		t.Error("zero Session must not fabricate reference-typed state")
	}
	if meta.Workspace.ProjectRoot != "" || meta.Workspace.ID != "" || meta.Workspace.Markers != nil || meta.Workspace.Labels != nil {
		t.Error("zero Workspace must stay entirely empty")
	}

	// Legacy identity fields keep their exact zero semantics.
	if meta.TraceID != "" || meta.ALegID != "" || meta.BLegID != "" || meta.AttemptSeq != 0 {
		t.Error("legacy identity fields must keep their zero value")
	}
}
