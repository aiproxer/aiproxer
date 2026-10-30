package keepwarm

// PolicyService is the narrow core service an authenticated admin adapter can
// call after resolving and validating proxy-owned A-leg authority. It stores no
// provider identity, cache key, handle, or controller.
type PolicyService struct {
	store    *PolicyStore
	registry *ManagerRegistry
	clock    Clock
}

func NewPolicyService(store *PolicyStore, registry *ManagerRegistry, clock Clock) *PolicyService {
	if clock == nil {
		clock = RealClock{}
	}
	return &PolicyService{store: store, registry: registry, clock: clock}
}

func (s *PolicyService) Disable(aLegID string) (SessionPolicy, error) {
	return s.store.DisableAndBroadcast(s.registry, aLegID, s.clock.Now())
}

// Clear restores global inheritance on the live generation managers and
// intentionally does not arm an epoch.
func (s *PolicyService) Clear(aLegID string) error {
	return s.store.ClearAndBroadcast(s.registry, aLegID)
}
func (s *PolicyService) Get(aLegID string) (SessionPolicy, bool) { return s.store.Get(aLegID) }
func (s *PolicyService) ForgetSession(aLegID string)             { s.store.Forget(aLegID) }
