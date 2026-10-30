package anthropicmessages

import (
	"context"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func (s *msgStream) captureEvidence(ev aipapi.Event) {
	if s.cache == nil {
		return
	}
	if ev.UsagePresence.CacheReadTokens && ev.CacheReadTokens > 0 {
		v := int64(ev.CacheReadTokens)
		s.cache.evidence.CacheReadTokens = &v
	}
	if ev.UsagePresence.CacheWriteTokens && ev.CacheWriteTokens > 0 {
		v := int64(ev.CacheWriteTokens)
		s.cache.evidence.CacheWriteTokens = &v
	}
	if ev.UsagePresence.InputTokens && ev.InputTokens > 0 {
		v := int64(ev.InputTokens)
		s.cache.evidence.InputTokens = &v
	}
	if ev.UsagePresence.OutputTokens && ev.OutputTokens > 0 {
		v := int64(ev.OutputTokens)
		s.cache.evidence.OutputTokens = &v
	}
	if ev.UsagePresence.InputTokens || ev.UsagePresence.OutputTokens || ev.UsagePresence.CacheReadTokens || ev.UsagePresence.CacheWriteTokens {
		s.cache.evidence.TotalTokens = totalInt64Anthropic(s.cache.evidence.InputTokens, s.cache.evidence.OutputTokens, s.cache.evidence.CacheReadTokens, s.cache.evidence.CacheWriteTokens)
	}
}

func totalInt64(a, b *int64) *int64 {
	if a == nil && b == nil {
		return nil
	}
	var total int64
	if a != nil {
		total += *a
	}
	if b != nil {
		total += *b
	}
	return &total
}

func totalInt64Anthropic(input, output, cacheRead, cacheWrite *int64) *int64 {
	has := false
	var total int64
	for _, v := range []*int64{input, output, cacheRead, cacheWrite} {
		if v != nil {
			total += *v
			has = true
		}
	}
	if !has {
		return nil
	}
	return &total
}

func (s *msgStream) completeCacheObservation() {
	if s.cache == nil || s.cache.hook == nil {
		return
	}
	if s.cache.evidence.CacheReadTokens == nil && s.cache.evidence.CacheWriteTokens == nil {
		s.cache.buffer.Discard()
		return
	}
	hasRead := s.cache.evidence.CacheReadTokens != nil && *s.cache.evidence.CacheReadTokens > 0
	hasWrite := s.cache.evidence.CacheWriteTokens != nil && *s.cache.evidence.CacheWriteTokens > 0
	if !hasRead && !hasWrite {
		s.cache.buffer.Discard()
		return
	}
	observedAt := s.cache.observedAt
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	obs, err := s.cache.hook(context.Background(), CacheObservation{
		Lineage:      s.cache.lineage,
		Model:        s.cache.renewal.Model,
		TTL:          s.cache.ttl,
		Renewal:      s.cache.renewal,
		Evidence:     s.cache.evidence,
		ObservedAt:   observedAt,
		CredentialID: s.cache.credentialID,
	})
	if err != nil {
		s.cache.buffer.Discard()
		return
	}
	if err := s.cache.buffer.Add(obs); err != nil {
		s.cache.buffer.Discard()
		return
	}
	s.cache.buffer.Commit()
}
