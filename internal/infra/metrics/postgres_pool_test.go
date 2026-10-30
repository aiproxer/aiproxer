package metrics

import (
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
)

func gatheredSample(t *testing.T, reg *prometheus.Registry, name string) (float64, dto.MetricType) {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		if len(mf.GetMetric()) == 0 {
			t.Fatalf("no samples for %s", name)
		}
		m := mf.GetMetric()[0]
		switch mf.GetType() {
		case dto.MetricType_GAUGE:
			return m.GetGauge().GetValue(), mf.GetType()
		case dto.MetricType_COUNTER:
			return m.GetCounter().GetValue(), mf.GetType()
		default:
			t.Fatalf("unexpected type %v for %s", mf.GetType(), name)
		}
	}
	t.Fatalf("metric %s not gathered", name)
	return 0, dto.MetricType_UNTYPED
}

func TestPostgresPoolPromAggregatesAcrossPools(t *testing.T) {
	reg := prometheus.NewRegistry()
	var mu sync.Mutex
	snapshot := []sql.DBStats{
		{OpenConnections: 7, InUse: 5, Idle: 2, MaxOpenConnections: 10, WaitCount: 11, WaitDuration: 3 * time.Second},
		{OpenConnections: 3, InUse: 1, Idle: 2, MaxOpenConnections: 4, WaitCount: 4, WaitDuration: 1 * time.Second},
	}
	source := func() []sql.DBStats {
		mu.Lock()
		defer mu.Unlock()
		out := make([]sql.DBStats, len(snapshot))
		copy(out, snapshot)
		return out
	}
	p := RegisterPostgresPoolProm(reg, source)

	if got, typ := gatheredSample(t, reg, "aip_postgres_pool_open_connections"); got != 10 || typ != dto.MetricType_GAUGE {
		t.Fatalf("open=%v type=%v want 10 gauge", got, typ)
	}
	if got, typ := gatheredSample(t, reg, "aip_postgres_pool_in_use_connections"); got != 6 || typ != dto.MetricType_GAUGE {
		t.Fatalf("in_use=%v type=%v want 6 gauge", got, typ)
	}
	if got, typ := gatheredSample(t, reg, "aip_postgres_pool_idle_connections"); got != 4 || typ != dto.MetricType_GAUGE {
		t.Fatalf("idle=%v type=%v want 4 gauge", got, typ)
	}
	if got, typ := gatheredSample(t, reg, "aip_postgres_pool_max_open_connections"); got != 14 || typ != dto.MetricType_GAUGE {
		t.Fatalf("max_open=%v type=%v want 14 gauge", got, typ)
	}
	if got, typ := gatheredSample(t, reg, "aip_postgres_pool_wait_total"); got != 15 || typ != dto.MetricType_COUNTER {
		t.Fatalf("wait=%v type=%v want 15 counter", got, typ)
	}
	if got, typ := gatheredSample(t, reg, "aip_postgres_pool_wait_duration_seconds_total"); got != 4 || typ != dto.MetricType_COUNTER {
		t.Fatalf("wait_seconds=%v type=%v want 4 counter", got, typ)
	}
	// Scrape-driven: changing the snapshot changes the next gathered value.
	mu.Lock()
	snapshot[0].OpenConnections = 1
	snapshot[1].OpenConnections = 1
	mu.Unlock()
	if got, _ := gatheredSample(t, reg, "aip_postgres_pool_open_connections"); got != 2 {
		t.Fatalf("open after re-scrape=%v want 2", got)
	}
	_ = p
}

func TestPostgresPoolPromNilSourceEmitsZeroedSeries(t *testing.T) {
	reg := prometheus.NewRegistry()
	p := RegisterPostgresPoolProm(reg, nil)
	if got, _ := gatheredSample(t, reg, "aip_postgres_pool_open_connections"); got != 0 {
		t.Fatalf("open=%v want 0", got)
	}
	if got, _ := gatheredSample(t, reg, "aip_postgres_pool_max_open_connections"); got != 0 {
		t.Fatalf("max_open=%v want 0", got)
	}
	if n := testutil.CollectAndCount(p, "aip_postgres_pool_open_connections"); n != 1 {
		t.Fatalf("expected 1 sample, got %d", n)
	}
}
