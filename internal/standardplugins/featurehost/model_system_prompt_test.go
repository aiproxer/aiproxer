package featurehost

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/continuity/bunstore"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/infra/controlplane/observers"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/plugins/features/modelsystemprompt"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"
)

func promptRegistration(t *testing.T) []aipsdk.Registration {
	t.Helper()
	var node yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte("rules:\n  - id: first\n    model_pattern: '^logical$'\n    append: 'private sentinel'\n  - id: second\n    model_pattern: 'logical'\n    append: 'second overlay'\n"), &node))
	return []aipsdk.Registration{{Kind: aipsdk.PluginKindFeature, ID: "model-system-prompt", Enabled: true, Config: aipsdk.ConfigPayload{Node: node}}}
}

func TestModelSystemPrompt_ProducerAtomicLazyAndContentFree(t *testing.T) {
	var logs bytes.Buffer
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{MaxLegs: 2})
	require.NoError(t, err)
	wrapped := observers.NewB2BUAStoreDecorator(observers.B2BUAStoreDecoratorConfig{Delegate: st})
	r, err := NewProcess(t.Context(), ProcessInput{Logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})), ContinuityStore: wrapped})
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Close() })
	leg, err := st.CreateALeg(t.Context(), "first")
	require.NoError(t, err)
	mutations := 0
	observer := promptMutationObserver{observe: func() {
		snap, err := r.ConversationReader().Snapshot(t.Context(), leg.ALegID)
		require.NoError(t, err)
		require.Len(t, snap.Steering, 2, "observations must see the committed whole batch")
		mutations++
	}}
	out, err := r.CompileGeneration(t.Context(), GenerationInput{Registrations: promptRegistration(t), ConversationObserver: observer})
	require.NoError(t, err)
	require.NotNil(t, out.CorePorts.ConversationBootstrap)
	resolutions := 0
	resolve := func() (runtime.InitialModelIntent, error) {
		resolutions++
		return runtime.InitialModelIntent{Model: "logical"}, nil
	}
	cause, err := out.CorePorts.ConversationBootstrap(t.Context(), leg.ALegID, nil, resolve)
	require.NoError(t, err)
	require.Equal(t, modelsystemprompt.CauseSystemPrompt, cause, "a newly committed overlay batch changes the effective instructions")
	require.Equal(t, 2, mutations)
	snap, err := r.ConversationReader().Snapshot(t.Context(), leg.ALegID)
	require.NoError(t, err)
	require.Len(t, snap.Steering, 2)
	require.Equal(t, "private sentinel", snap.Steering[0].Message.Text)
	require.Equal(t, "second overlay", snap.Steering[1].Message.Text)
	cause, err = out.CorePorts.ConversationBootstrap(t.Context(), leg.ALegID, nil, func() (runtime.InitialModelIntent, error) {
		t.Error("reused completion resolved")
		return runtime.InitialModelIntent{}, errors.New("forbidden")
	})
	require.NoError(t, err)
	require.Empty(t, cause, "a reused completion changed nothing")
	require.Equal(t, 1, resolutions)
	require.Equal(t, 2, mutations)
	old, err := st.CreateALeg(t.Context(), "allocated")
	require.NoError(t, err)
	_, err = st.NextBLeg(t.Context(), old.ALegID)
	require.NoError(t, err)
	cause, err = out.CorePorts.ConversationBootstrap(t.Context(), old.ALegID, nil, func() (runtime.InitialModelIntent, error) {
		t.Error("preallocated leg resolved")
		return runtime.InitialModelIntent{}, nil
	})
	require.NoError(t, err)
	require.Empty(t, cause, "a preexisting-skip decision changed nothing")
	snap, err = r.ConversationReader().Snapshot(t.Context(), old.ALegID)
	require.NoError(t, err)
	require.Empty(t, snap.Steering)
	require.Equal(t, 2, mutations)
	_, err = st.CreateALeg(t.Context(), "evict")
	require.NoError(t, err)
	snap, err = r.ConversationReader().Snapshot(t.Context(), leg.ALegID)
	require.NoError(t, err)
	require.Empty(t, snap.Steering)
	require.NotContains(t, logs.String(), "private sentinel")
	require.NotContains(t, logs.String(), "second overlay")
	require.Contains(t, logs.String(), "preexisting_skip")
	require.Contains(t, logs.String(), "reused")
}

type promptMutationObserver struct {
	conversationview.NopObserver
	observe func()
}

func (o promptMutationObserver) OnSteeringMutation(conversationview.CacheDiscontinuityKind, conversationview.PlacementKind) {
	o.observe()
}

func TestModelSystemPrompt_EmptyDecisionsAndFailures(t *testing.T) {
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	require.NoError(t, err)
	r, err := NewProcess(t.Context(), ProcessInput{Logger: slog.Default(), ContinuityStore: st})
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Close() })
	out, err := r.CompileGeneration(t.Context(), GenerationInput{Registrations: promptRegistration(t)})
	require.NoError(t, err)
	// The ambiguous row carries a model the configured rules DO match, so the empty
	// decision can only come from treating ambiguity as a skip, not from a model
	// that simply matches nothing.
	for _, intent := range []runtime.InitialModelIntent{{Model: "other"}, {Model: "logical", Ambiguous: true}} {
		leg, err := st.CreateALeg(t.Context(), "")
		require.NoError(t, err)
		cause, err := out.CorePorts.ConversationBootstrap(t.Context(), leg.ALegID, nil, func() (runtime.InitialModelIntent, error) { return intent, nil })
		require.NoError(t, err)
		require.Empty(t, cause, "an empty decision changed nothing")
		cause, err = out.CorePorts.ConversationBootstrap(t.Context(), leg.ALegID, nil, func() (runtime.InitialModelIntent, error) {
			t.Error("empty completion reran")
			return runtime.InitialModelIntent{Model: "logical"}, nil
		})
		require.NoError(t, err)
		require.Empty(t, cause, "a reused empty completion changed nothing")
		snap, err := r.ConversationReader().Snapshot(t.Context(), leg.ALegID)
		require.NoError(t, err)
		require.Empty(t, snap.Steering)
	}
	leg, err := st.CreateALeg(t.Context(), "")
	require.NoError(t, err)
	_, err = out.CorePorts.ConversationBootstrap(t.Context(), leg.ALegID, nil, func() (runtime.InitialModelIntent, error) {
		return runtime.InitialModelIntent{}, errors.New("private sentinel")
	})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "private sentinel")
	cause, err := out.CorePorts.ConversationBootstrap(t.Context(), leg.ALegID, nil, func() (runtime.InitialModelIntent, error) {
		return runtime.InitialModelIntent{Model: "logical"}, nil
	})
	require.NoError(t, err)
	require.Equal(t, modelsystemprompt.CauseSystemPrompt, cause, "the retry that committed the batch records the cause")
}

func TestModelSystemPrompt_PrerequisitePairingAndDefaultOff(t *testing.T) {
	regs := promptRegistration(t)
	var missing *Runtime
	_, err := missing.CompileGeneration(t.Context(), GenerationInput{Registrations: regs})
	require.Error(t, err)
	r, err := NewProcess(t.Context(), ProcessInput{Logger: slog.Default()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Close() })
	_, err = r.CompileGeneration(t.Context(), GenerationInput{Registrations: regs})
	require.Error(t, err)
	regs[0].Enabled = false
	out, err := r.CompileGeneration(t.Context(), GenerationInput{Registrations: regs})
	require.NoError(t, err)
	require.Nil(t, out.CorePorts.ConversationBootstrap)
	actual, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	require.NoError(t, err)
	other, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	require.NoError(t, err)
	mispaired, err := NewProcess(t.Context(), ProcessInput{Logger: slog.Default(), ContinuityStore: &mispairedPromptStore{MemoryStore: actual, authority: other}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mispaired.Close() })
	_, err = mispaired.CompileGeneration(t.Context(), GenerationInput{Registrations: promptRegistration(t)})
	require.Error(t, err, "an unknown custom wrapper cannot certify that its callback guards its NextBLeg")
	open := func(name string) *bunstore.Store {
		sqlDB, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), name)+"?_pragma=foreign_keys(ON)")
		require.NoError(t, err)
		bdb, err := db.NewBunDB(sqlDB, db.DialectSQLite)
		require.NoError(t, err)
		t.Cleanup(func() { _ = bdb.Close() })
		store, err := bunstore.New(bdb)
		require.NoError(t, err)
		return store
	}
	left, right := open("left.db"), open("right.db")
	for _, tc := range []struct {
		name       string
		continuity b2bua.Store
		matching   bool
	}{{"same", left, true}, {"different", right, false}} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := NewProcess(t.Context(), ProcessInput{Logger: slog.Default(), ContinuityStore: tc.continuity, BunDB: left.DB()})
			require.NoError(t, err)
			t.Cleanup(func() { _ = r.Close() })
			regs := promptRegistration(t)
			out, err := r.CompileGeneration(t.Context(), GenerationInput{Registrations: regs})
			if !tc.matching {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			leg, err := left.CreateALeg(t.Context(), "")
			require.NoError(t, err)
			cause, err := out.CorePorts.ConversationBootstrap(t.Context(), leg.ALegID, nil, func() (runtime.InitialModelIntent, error) { return runtime.InitialModelIntent{Model: "logical"}, nil })
			require.NoError(t, err)
			require.Equal(t, modelsystemprompt.CauseSystemPrompt, cause)
			snap, err := r.ConversationReader().Snapshot(t.Context(), leg.ALegID)
			require.NoError(t, err)
			require.Len(t, snap.Steering, 2)
		})
	}
}

type mispairedPromptStore struct {
	*b2bua.MemoryStore
	authority *b2bua.MemoryStore
}

func (s *mispairedPromptStore) Unwrap() any { return s.MemoryStore }
func (s *mispairedPromptStore) WithBLegAllocationAuthority(ctx context.Context, id string, apply func(bool) error) error {
	return s.authority.WithBLegAllocationAuthority(ctx, id, apply)
}

// TestModelSystemPrompt_ReKeyedRegistrationPublishesProducer pins the same
// registry-factory-key resolution the standard distribution relies on: a
// registration whose id is re-keyed to the canonical prompt factory still
// publishes the bootstrap producer, so a re-keyed distribution cannot silently
// lose persistent steering.
func TestModelSystemPrompt_ReKeyedRegistrationPublishesProducer(t *testing.T) {
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{MaxLegs: 2})
	require.NoError(t, err)
	wrapped := observers.NewB2BUAStoreDecorator(observers.B2BUAStoreDecoratorConfig{Delegate: st})
	r, err := NewProcess(t.Context(), ProcessInput{Logger: slog.Default(), ContinuityStore: wrapped})
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Close() })
	leg, err := st.CreateALeg(t.Context(), "first")
	require.NoError(t, err)

	registrations := promptRegistration(t)
	registrations[0].ID = "rekeyed-model-system-prompt"
	registrations[0].FactoryKind = "model-system-prompt"
	out, err := r.CompileGeneration(t.Context(), GenerationInput{Registrations: registrations})
	require.NoError(t, err)
	require.NotNil(t, out.CorePorts.ConversationBootstrap, "a re-keyed registration must still publish the producer")
	cause, err := out.CorePorts.ConversationBootstrap(t.Context(), leg.ALegID, nil, func() (runtime.InitialModelIntent, error) {
		return runtime.InitialModelIntent{Model: "logical"}, nil
	})
	require.NoError(t, err)
	require.Equal(t, modelsystemprompt.CauseSystemPrompt, cause)
	snap, err := r.ConversationReader().Snapshot(t.Context(), leg.ALegID)
	require.NoError(t, err)
	require.Len(t, snap.Steering, 2)
}

// TestModelSystemPrompt_InvalidConfigRejectsCandidate pins the failure path at
// the composition seam: a prompt subtree this build cannot decode or compile
// rejects the candidate generation and publishes no producer, so a typo cannot
// be silently ignored while persistent steering goes missing.
func TestModelSystemPrompt_InvalidConfigRejectsCandidate(t *testing.T) {
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	require.NoError(t, err)
	wrapped := observers.NewB2BUAStoreDecorator(observers.B2BUAStoreDecoratorConfig{Delegate: st})
	r, err := NewProcess(t.Context(), ProcessInput{Logger: slog.Default(), ContinuityStore: wrapped})
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Close() })

	for _, tc := range []struct {
		name  string
		rules string
	}{
		{name: "unknown field", rules: "rules: []\nnot_a_key: 1\n"},
		{name: "invalid model pattern", rules: "rules:\n  - id: broken\n    model_pattern: '['\n    append: 'text'\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var node yaml.Node
			require.NoError(t, yaml.Unmarshal([]byte(tc.rules), &node))
			out, err := r.CompileGeneration(t.Context(), GenerationInput{Registrations: []aipsdk.Registration{{
				Kind:    aipsdk.PluginKindFeature,
				ID:      "model-system-prompt",
				Enabled: true,
				Config:  aipsdk.ConfigPayload{Node: node},
			}}})
			require.Error(t, err)
			require.Nil(t, out.CorePorts.ConversationBootstrap)
		})
	}
}
