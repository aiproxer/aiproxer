package compaction_test

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	featurecontinuity "github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/compaction"
	"gopkg.in/yaml.v3"
)

func TestSchedulerBoundsFromConfig_Defaults(t *testing.T) {
	t.Parallel()
	bounds := compaction.SchedulerBoundsFromConfig(nil)
	if bounds.Workers != featurecontinuity.DefaultMaxConcurrency {
		t.Fatalf("Workers: got %d, want %d", bounds.Workers, featurecontinuity.DefaultMaxConcurrency)
	}
	if bounds.QueueCapacity != featurecontinuity.DefaultQueueCapacity {
		t.Fatalf("QueueCapacity: got %d, want %d", bounds.QueueCapacity, featurecontinuity.DefaultQueueCapacity)
	}
	if bounds.MaxResults != featurecontinuity.DefaultResultMaxCount {
		t.Fatalf("MaxResults: got %d, want %d", bounds.MaxResults, featurecontinuity.DefaultResultMaxCount)
	}
	if bounds.ResultTTL != featurecontinuity.DefaultPendingResultTTL {
		t.Fatalf("ResultTTL: got %v, want %v", bounds.ResultTTL, featurecontinuity.DefaultPendingResultTTL)
	}
	if bounds.JobTimeout != featurecontinuity.DefaultExtractorTimeout {
		t.Fatalf("JobTimeout: got %v, want %v", bounds.JobTimeout, featurecontinuity.DefaultExtractorTimeout)
	}
	if bounds.MaxResultBytes != featurecontinuity.DefaultResultMaxBytes {
		t.Fatalf("MaxResultBytes: got %d, want %d", bounds.MaxResultBytes, featurecontinuity.DefaultResultMaxBytes)
	}
}

func TestSchedulerBoundsFromConfig_Custom(t *testing.T) {
	t.Parallel()
	yamlDoc := `
plugins:
  features:
    - id: compaction-continuity
      enabled: true
      config:
        worker:
          max_concurrency: 5
          queue_capacity: 50
        result:
          max_count: 200
          ttl: 5m
          max_bytes: 4194304
        extractor:
          inherit: true
          timeout: 45s
`
	var cfg config.Config
	if err := yaml.Unmarshal([]byte(yamlDoc), &cfg); err != nil {
		t.Fatalf("yaml unmarshal: %v", err)
	}

	bounds := compaction.SchedulerBoundsFromConfig(&cfg)
	if bounds.Workers != 5 {
		t.Fatalf("Workers: got %d, want 5", bounds.Workers)
	}
	if bounds.QueueCapacity != 50 {
		t.Fatalf("QueueCapacity: got %d, want 50", bounds.QueueCapacity)
	}
	if bounds.MaxResults != 200 {
		t.Fatalf("MaxResults: got %d, want 200", bounds.MaxResults)
	}
	if bounds.ResultTTL != 5*time.Minute {
		t.Fatalf("ResultTTL: got %v, want 5m", bounds.ResultTTL)
	}
	if bounds.JobTimeout != 45*time.Second {
		t.Fatalf("JobTimeout: got %v, want 45s", bounds.JobTimeout)
	}
	if bounds.MaxResultBytes != 4194304 {
		t.Fatalf("MaxResultBytes: got %d, want 4194304", bounds.MaxResultBytes)
	}
}
