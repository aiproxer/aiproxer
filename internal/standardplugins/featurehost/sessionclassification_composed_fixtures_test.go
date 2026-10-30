package featurehost

// Shared composed-process fixtures for the standard featurehost session
// classification tests. They stand up a real process over a real on-disk SQLite
// database with a dedicated production metric registry, and read the committed
// durable state back through an independent store instance, so "no durable work"
// is observed as committed state rather than inferred from a counter.

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/uptrace/bun"
)

// hotPathProcess composes a real featurehost process over a real on-disk SQLite
// database with a dedicated production metric registry.
func hotPathProcess(tb testing.TB, name string) (*Runtime, *bun.DB, *prometheus.Registry) {
	tb.Helper()
	dsn := certificationSQLiteDSN(filepath.Join(tb.TempDir(), name))
	database := certificationOpenSQLite(tb, dsn)
	registry := prometheus.NewRegistry()
	rt, err := NewProcess(context.Background(), ProcessInput{
		Logger: slog.Default(), BunDB: database, MetricsRegistry: registry,
	})
	if err != nil {
		tb.Fatalf("NewProcess: %v", err)
	}
	tb.Cleanup(func() {
		if err := rt.Close(); err != nil {
			tb.Errorf("close process: %v", err)
		}
	})
	return rt, database, registry
}

// hotPathClassificationObservations is the census of every classification-specific
// observation the production collector can emit, read as a whole so a caller can
// assert that none of them is present.
func hotPathClassificationObservations(tb testing.TB, registry *prometheus.Registry) map[string]float64 {
	tb.Helper()
	families, err := registry.Gather()
	if err != nil {
		tb.Fatalf("gather feature metrics: %v", err)
	}
	observed := map[string]float64{}
	for _, family := range families {
		switch family.GetName() {
		case "aip_session_classification_evaluations_total",
			"aip_session_classification_transitions_total",
			"aip_session_classification_remote_total",
			"aip_session_classification_remote_seconds",
			"aip_session_classification_store_total":
		default:
			continue
		}
		for _, metric := range family.GetMetric() {
			labels := make([]string, 0, len(metric.GetLabel()))
			for _, label := range metric.GetLabel() {
				labels = append(labels, label.GetName()+"="+label.GetValue())
			}
			key := family.GetName() + "{" + joinHotPathLabels(labels) + "}"
			switch {
			case metric.GetCounter() != nil:
				observed[key] += metric.GetCounter().GetValue()
			case metric.GetHistogram() != nil:
				observed[key] += float64(metric.GetHistogram().GetSampleCount())
			case metric.GetGauge() != nil:
				observed[key] += metric.GetGauge().GetValue()
			}
		}
	}
	return observed
}

func joinHotPathLabels(labels []string) string {
	return strings.Join(labels, ",")
}

// certificationRowIsPositive reports whether a durable positive exists for a
// session, reading committed state through an independent store instance.
func certificationRowIsPositive(tb testing.TB, database *bun.DB, sessionID string) bool {
	tb.Helper()
	row, found := certificationLoadRow(tb, database, sessionID)
	return found && row.Classification.IsCodingAgent()
}

// hotPathClassificationTableExists reports whether the feature's own durable
// schema exists. Only an enabled generation's lifecycle initializes feature
// state, so a disabled process must not have created it.
func hotPathClassificationTableExists(tb testing.TB, database *bun.DB) bool {
	tb.Helper()
	var names []string
	if err := database.NewRaw(
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'session_classification'`,
	).Scan(context.Background(), &names); err != nil && !errors.Is(err, sql.ErrNoRows) {
		tb.Fatalf("inspect sqlite_master: %v", err)
	}
	return len(names) > 0
}
