package runtimebundle

import (
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

// closeErrorStore is a control-plane store whose closer fails, used to prove a
// construction failure keeps both the primary configuration error and the
// cleanup error that followed it.
type closeErrorStore struct {
	*fakeRetentionStore
	err error
}

func (s *closeErrorStore) Close() error { return s.err }

// TestBuildControlPlaneRuntimeJoinsCleanupError proves both early validation
// failures that dispose the freshly opened store surface the cleanup failure
// instead of discarding it behind the primary error.
func TestBuildControlPlaneRuntimeJoinsCleanupError(t *testing.T) {
	t.Parallel()
	closeErr := errors.New("control-plane store close failed")
	tests := []struct {
		name  string
		query config.ControlPlaneQueryConfig
		ret   config.ControlPlaneRetentionConfig
		want  string
	}{
		{
			name:  "invalid query window",
			query: config.ControlPlaneQueryConfig{Enabled: true, MaxTimeWindow: "not-a-duration"},
			want:  "control_plane.query.max_time_window",
		},
		{
			name: "invalid retention window",
			ret:  config.ControlPlaneRetentionConfig{Enabled: true, Window: "not-a-duration"},
			want: "control plane retention",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{ControlPlane: config.ControlPlaneConfig{
				Enabled:         true,
				Store:           "memory",
				RecordingPolicy: "best_effort",
				Query:           tt.query,
				Retention:       tt.ret,
			}}
			store := &closeErrorStore{fakeRetentionStore: &fakeRetentionStore{}, err: closeErr}
			_, err := buildControlPlaneRuntime(controlPlaneBuildInput{Cfg: cfg, StoreOverride: store})
			if err == nil {
				t.Fatal("expected control-plane construction to fail")
			}
			if !errors.Is(err, closeErr) {
				t.Fatalf("error = %v, want it to preserve the store cleanup failure %v", err, closeErr)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want it to retain the primary failure %q", err, tt.want)
			}
		})
	}
}
