package compaction

import (
	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/internal/core/config"
	featurecontinuity "github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// SchedulerBoundsFromConfig translates compaction continuity feature config
// into generic auxiliary scheduler bounds.
func SchedulerBoundsFromConfig(cfg *config.Config) auxreq.SchedulerConfig {
	schedulerCfg := auxreq.SchedulerConfig{
		Workers:        featurecontinuity.DefaultMaxConcurrency,
		QueueCapacity:  featurecontinuity.DefaultQueueCapacity,
		MaxResults:     featurecontinuity.DefaultResultMaxCount,
		ResultTTL:      featurecontinuity.DefaultPendingResultTTL,
		JobTimeout:     featurecontinuity.DefaultExtractorTimeout,
		MaxResultBytes: featurecontinuity.DefaultResultMaxBytes,
	}
	for _, reg := range config.RegistrationsFromConfig(cfg) {
		if reg.Kind != aipsdk.PluginKindFeature || reg.RegistryFactoryKey() != featurecontinuity.ID {
			continue
		}
		featureCfg, err := featurecontinuity.DecodeConfig(reg.Config.Node)
		if err != nil {
			continue
		}
		schedulerCfg.Workers, schedulerCfg.QueueCapacity = featureCfg.Worker.MaxConcurrency, featureCfg.Worker.QueueCapacity
		schedulerCfg.MaxResults, schedulerCfg.ResultTTL = featureCfg.Result.MaxCount, featureCfg.Result.TTL
		schedulerCfg.JobTimeout, schedulerCfg.MaxResultBytes = featureCfg.Extractor.Timeout, featureCfg.Result.MaxBytes
		break
	}
	return schedulerCfg
}
