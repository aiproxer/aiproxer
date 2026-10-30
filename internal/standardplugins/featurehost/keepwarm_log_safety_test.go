package featurehost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/billingport"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/logging"
	"github.com/aiproxer/aiproxer/internal/plugins/features/keepwarm"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
)

// TestKeepwarmAccountingFailureLogIsExplicitAndSecretSafe proves the durable
// maintenance observer failure stays observable through a stable reason while
// the untrusted observer error (which may carry credentials or a DSN) is never
// echoed, and the returned error keeps its identity for the caller.
func TestKeepwarmAccountingFailureLogIsExplicitAndSecretSafe(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, text, marker string }{
		{"bearer_header", "Authorization: Bearer r3synthetic-bearer-value", "r3synthetic-bearer-value"},
		{"basic_header", "Authorization: Basic r3synthetic-basic-value", "r3synthetic-basic-value"},
		{"password_dsn", "postgres://proxy:r3synthetic-dsn-value@db.invalid:5432/billing", "r3synthetic-dsn-value"},
		{"plain_secret_assignment", "secret=r3synthetic-assignment-value", "r3synthetic-assignment-value"},
		{"control_injection", "storage failed\nlevel=ERROR r3synthetic-injected-value", "r3synthetic-injected-value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			logger, err := logging.NewLogger(config.LoggingConfig{Level: "info", Format: "json"}, &out)
			if err != nil {
				t.Fatalf("logger: %v", err)
			}
			want := fmt.Errorf("maintenance sink rejected request: %s", tc.text)
			observer := billingport.ProviderMaintenanceUsageObserverFunc(func(context.Context, billingport.ProviderMaintenanceUsage) error {
				return want
			})
			hooks := keepwarmAccountingHooks(observer, logger)
			got := hooks.Accounting(context.Background(), keepwarm.RenewalRecord{
				OperationID: "operation-1",
				BackendID:   "backend",
				ModelID:     "model",
				Accounting:  &promptcache.AccountingEvidence{},
			})
			if !errors.Is(got, want) {
				t.Fatalf("observer error identity lost: %v", got)
			}
			logged := out.String()
			if !strings.Contains(logged, "keepwarm provider maintenance accounting failed") ||
				!strings.Contains(logged, "\"reason\":\"accounting_failed\"") ||
				!strings.Contains(logged, "\"operation_id\":\"operation-1\"") {
				t.Fatalf("accounting failure lost its explicit message/reason/correlation: %q", logged)
			}
			if strings.Contains(logged, tc.marker) {
				t.Fatalf("accounting failure exposed untrusted credential text %q", tc.marker)
			}
		})
	}
}
