package evidence

import "testing"

func TestFinish_CannotCertifyPlanOnlyOrFailedStep(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name     string
		manifest Manifest
		want     string
	}{
		{name: "plan only", manifest: Manifest{Scope: Scope{PlanOnly: true}}, want: "skipped"},
		{name: "empty selected scope", manifest: Manifest{Skips: []Skip{{Target: "default tests", Reason: "no packages selected"}}}, want: "skipped"},
		{name: "failed recorded step", manifest: Manifest{Steps: []Step{{Result: OutcomeFailed}}}, want: OutcomeFailed},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			scenario.manifest.Finish(nil, "")
			if scenario.manifest.Outcome != scenario.want {
				t.Fatalf("uncertified run outcome=%s, want %s", scenario.manifest.Outcome, scenario.want)
			}
		})
	}
}
