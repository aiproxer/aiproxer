package runtime

import (
	"reflect"
	"testing"
)

func TestExecutor_BillingCollectorStateGrowth(t *testing.T) {
	// Task 3.4: Adapt the Phase 0 retention test to assert executor does not retain
	// completed-call state without relying on deleted implementation names.
	// We verify that the Executor struct itself does not retain any maps or registries
	// that could grow with the number of calls, as state is now request-scoped.
	t.Parallel()

	executor := &Executor{}
	val := reflect.ValueOf(executor).Elem()
	typ := val.Type()

	for field := range typ.Fields() {
		// Check for any map fields directly on Executor
		if field.Type.Kind() == reflect.Map {
			t.Errorf("Forbidden map field found on Executor: %s", field.Name)
		}
		// Assert that billingColl or billingOnce no longer exist
		if field.Name == "billingColl" || field.Name == "billingOnce" {
			t.Errorf("Obsolete billing field found on Executor: %s", field.Name)
		}
	}
}
