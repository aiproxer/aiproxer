package engine_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/secretguard/engine"
	"github.com/aiproxer/aiproxer/internal/testkit"
)

func TestComposeSource_disabledNeverTouchesEnv(t *testing.T) {
	t.Parallel()
	env := &panicEnvironment{}
	src, err := engine.ComposeSource(engine.ModeSingleUser, false, env, engine.SingleUserOptions{
		IncludePopularEnv: true,
		IncludeEnv:        []string{"OPENAI_API_KEY"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if src.EntryCount() != 0 {
		t.Fatalf("disabled EntryCount=%d", src.EntryCount())
	}
	_ = src.MatcherResolver()
	if env.calls != 0 {
		t.Fatalf("disabled must not touch Environment; calls=%d", env.calls)
	}
}

func TestComposeSource_multiUserNeverTouchesEnv(t *testing.T) {
	t.Parallel()
	env := &panicEnvironment{}
	src, err := engine.ComposeSource(engine.ModeMultiUser, true, env, engine.SingleUserOptions{
		IncludePopularEnv: true,
		IncludeEnv:        []string{"OPENAI_API_KEY"},
		MinSecretBytes:    8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if src.AccessMode() != engine.ModeMultiUser {
		t.Fatalf("mode=%v", src.AccessMode())
	}
	if src.EntryCount() != 0 {
		t.Fatalf("multi-user EntryCount=%d", src.EntryCount())
	}
	_ = src.MatcherResolver()
	if env.calls != 0 {
		t.Fatalf("multi-user must not touch Environment; calls=%d", env.calls)
	}
}

func TestComposeSource_singleUserLoadsSparse(t *testing.T) {
	t.Parallel()
	env := &countingEnvironment{vals: map[string]string{
		"OPENAI_API_KEY":   testkit.SyntheticOpenAIAPIKey,
		"OPENAI_API_KEY_7": testkit.SyntheticGeminiAPIKey,
	}}
	src, err := engine.ComposeSource(engine.ModeSingleUser, true, env, engine.SingleUserOptions{
		MinSecretBytes: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if src.AccessMode() != engine.ModeSingleUser {
		t.Fatalf("mode=%v", src.AccessMode())
	}
	if src.EntryCount() < 2 {
		t.Fatalf("EntryCount=%d", src.EntryCount())
	}
	if env.snapshotCalls == 0 {
		t.Fatal("single-user must Snapshot")
	}
}
