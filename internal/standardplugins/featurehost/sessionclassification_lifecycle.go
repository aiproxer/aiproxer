package featurehost

import (
	"context"
	"sync"

	featureclassification "github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification"
	hostclassification "github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/sessionclassification"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/plugin"
)

type sessionClassificationGenerationLifecycle struct {
	mu      sync.Mutex
	holder  *hostclassification.StateHolder
	started bool
	stopped bool
}

var _ aipplugin.Lifecycle = (*sessionClassificationGenerationLifecycle)(nil)

func (l *sessionClassificationGenerationLifecycle) Start(ctx context.Context) error {
	if l == nil || l.holder == nil {
		return hostclassification.ErrStateHolderConfig
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped {
		return hostclassification.ErrStateHolderClosed
	}
	if l.started {
		return nil
	}
	if err := l.holder.Acquire(ctx); err != nil {
		return err
	}
	l.started = true
	return nil
}

func (l *sessionClassificationGenerationLifecycle) Stop(context.Context) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	if l.stopped {
		l.mu.Unlock()
		return nil
	}
	l.stopped = true
	started := l.started
	l.started = false
	l.mu.Unlock()
	if started {
		l.holder.Release()
	}
	return nil
}

// SafeUnderCandidateOverlap reports that candidate generations share one
// process-owned holder and each lifecycle owns only its independent reference.
func (*sessionClassificationGenerationLifecycle) SafeUnderCandidateOverlap() bool { return true }

func (r *Runtime) sessionClassificationLifecycle(registrations []aipsdk.Registration) aipplugin.Lifecycle {
	if r == nil || r.sessionClassification == nil {
		return nil
	}
	if _, enabled := enabledSessionClassificationRegistration(registrations); !enabled {
		return nil
	}
	return &sessionClassificationGenerationLifecycle{holder: r.sessionClassification}
}

// enabledSessionClassificationRegistration reports the canonical outer-enabled
// session-classification feature registration. The outer Registration.Enabled
// flag stays authoritative: an absent entry and an outer-disabled entry both
// leave the feature absent, and the registry factory key is accepted alongside
// the registration id so re-keyed distributions resolve identically.
func enabledSessionClassificationRegistration(registrations []aipsdk.Registration) (aipsdk.Registration, bool) {
	for _, registration := range registrations {
		if registration.Kind != aipsdk.PluginKindFeature ||
			(registration.ID != featureclassification.ID && registration.RegistryFactoryKey() != featureclassification.ID) {
			continue
		}
		if !registration.Enabled {
			continue
		}
		return registration, true
	}
	return aipsdk.Registration{}, false
}
