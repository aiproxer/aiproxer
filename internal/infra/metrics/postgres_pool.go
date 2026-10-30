package metrics

import (
	"database/sql"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// PostgresPoolProm exports aggregate database/sql pool saturation for the
// registry-owned PostgreSQL pools. It is a scrape-driven prometheus.Collector:
// values are read live from the pool registry on every scrape, so no background
// goroutine or periodic sampling is required (and there is nothing to leak).
type PostgresPoolProm struct {
	source func() []sql.DBStats

	open        *prometheus.Desc
	idle        *prometheus.Desc
	inUse       *prometheus.Desc
	maxOpen     *prometheus.Desc
	waitTotal   *prometheus.Desc
	waitSeconds *prometheus.Desc
}

// RegisterPostgresPoolProm registers a Collector that reads pool stats from
// source on each scrape. source may be nil; the collector then emits zeroed
// series, which keeps the metric family present for dashboards.
func RegisterPostgresPoolProm(reg prometheus.Registerer, source func() []sql.DBStats) *PostgresPoolProm {
	p := &PostgresPoolProm{
		source: source,
		open:   prometheus.NewDesc(namespace+"_postgres_pool_open_connections", "Open PostgreSQL runtime connections.", nil, nil),
		idle:   prometheus.NewDesc(namespace+"_postgres_pool_idle_connections", "Idle PostgreSQL runtime connections.", nil, nil),
		inUse:  prometheus.NewDesc(namespace+"_postgres_pool_in_use_connections", "In-use PostgreSQL runtime connections.", nil, nil),
		maxOpen: prometheus.NewDesc(
			namespace+"_postgres_pool_max_open_connections",
			"Configured max open PostgreSQL runtime connections (summed across registry pools). Zero means unlimited in database/sql.",
			nil,
			nil,
		),
		waitTotal:   prometheus.NewDesc(namespace+"_postgres_pool_wait_total", "Cumulative PostgreSQL pool waits.", nil, nil),
		waitSeconds: prometheus.NewDesc(namespace+"_postgres_pool_wait_duration_seconds_total", "Cumulative PostgreSQL pool wait duration in seconds.", nil, nil),
	}
	reg.MustRegister(p)
	return p
}

// Describe implements prometheus.Collector with stable descriptors.
func (p *PostgresPoolProm) Describe(ch chan<- *prometheus.Desc) {
	if p == nil {
		return
	}
	ch <- p.open
	ch <- p.idle
	ch <- p.inUse
	ch <- p.maxOpen
	ch <- p.waitTotal
	ch <- p.waitSeconds
}

// Collect implements prometheus.Collector. It sums database/sql pool stats
// across every registry-owned pool and emits one aggregate sample per series.
func (p *PostgresPoolProm) Collect(ch chan<- prometheus.Metric) {
	if p == nil {
		return
	}
	var stats []sql.DBStats
	if p.source != nil {
		stats = p.source()
	}
	var open, idle, inUse, maxOpen int
	var waitCount int64
	var waitDuration time.Duration
	for _, s := range stats {
		open += s.OpenConnections
		idle += s.Idle
		inUse += s.InUse
		maxOpen += s.MaxOpenConnections
		waitCount += s.WaitCount
		waitDuration += s.WaitDuration
	}
	ch <- prometheus.MustNewConstMetric(p.open, prometheus.GaugeValue, float64(open))
	ch <- prometheus.MustNewConstMetric(p.idle, prometheus.GaugeValue, float64(idle))
	ch <- prometheus.MustNewConstMetric(p.inUse, prometheus.GaugeValue, float64(inUse))
	ch <- prometheus.MustNewConstMetric(p.maxOpen, prometheus.GaugeValue, float64(maxOpen))
	ch <- prometheus.MustNewConstMetric(p.waitTotal, prometheus.CounterValue, float64(waitCount))
	ch <- prometheus.MustNewConstMetric(p.waitSeconds, prometheus.CounterValue, waitDuration.Seconds())
}
