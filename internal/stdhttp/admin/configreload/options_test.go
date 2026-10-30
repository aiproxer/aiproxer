package configreload_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/accessmode"
	mgmtreload "github.com/aiproxer/aiproxer/internal/stdhttp/admin/configreload"
)

func TestAuth_LoopbackPostureValidation(t *testing.T) {
	t.Parallel()

	t.Run("default_loopback_local_trust", func(t *testing.T) {
		t.Parallel()
		err := mgmtreload.Options{Address: "127.0.0.1:0"}.Validate()
		if err != nil {
			t.Fatalf("want ok, got %v", err)
		}
	})

	t.Run("non_loopback_requires_opt_in_and_strong_auth", func(t *testing.T) {
		t.Parallel()
		err := mgmtreload.Options{Address: "0.0.0.0:9090", AuthMode: mgmtreload.AuthModeBearer, BearerToken: "test-management-secret"}.Validate()
		if err == nil || !strings.Contains(err.Error(), "AllowNonLoopback") {
			t.Fatalf("want AllowNonLoopback error, got %v", err)
		}
		err = mgmtreload.Options{
			Address:          "10.0.0.5:9090",
			AllowNonLoopback: true,
			AuthMode:         mgmtreload.AuthModeLocalTrust,
		}.Validate()
		if err == nil || !strings.Contains(err.Error(), "local_trust") {
			t.Fatalf("want local_trust rejection, got %v", err)
		}
		err = mgmtreload.Options{
			Address:          "10.0.0.5:9090",
			AllowNonLoopback: true,
			AuthMode:         mgmtreload.AuthModeBearer,
			BearerToken:      "test-management-secret",
		}.Validate()
		if err != nil {
			t.Fatalf("want ok, got %v", err)
		}
	})

	t.Run("multi_user_requires_strong_auth", func(t *testing.T) {
		t.Parallel()
		err := mgmtreload.Options{
			Address:    "127.0.0.1:0",
			AccessMode: accessmode.ModeMultiUser,
			AuthMode:   mgmtreload.AuthModeLocalTrust,
		}.Validate()
		if err == nil {
			t.Fatal("want multi_user local_trust rejection")
		}
		err = mgmtreload.Options{
			Address:     "127.0.0.1:0",
			AccessMode:  accessmode.ModeMultiUser,
			AuthMode:    mgmtreload.AuthModeBearer,
			BearerToken: "short",
		}.Validate()
		if err == nil || !strings.Contains(err.Error(), "16") {
			t.Fatalf("want short bearer rejection, got %v", err)
		}
		err = mgmtreload.Options{
			Address:     "127.0.0.1:0",
			AccessMode:  accessmode.ModeMultiUser,
			AuthMode:    mgmtreload.AuthModeBearer,
			BearerToken: "test-management-secret",
		}.Validate()
		if err != nil {
			t.Fatalf("want ok, got %v", err)
		}
	})
}
