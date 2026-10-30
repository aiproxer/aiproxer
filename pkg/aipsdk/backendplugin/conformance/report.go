package conformance

import "fmt"

// Result is one case outcome.
type Result struct {
	Name   string
	Passed bool
	Detail string
	Stable string
}

// Report aggregates conformance results.
type Report struct {
	Results []Result
}

// Ok reports whether every case passed.
func (r Report) Ok() bool {
	for _, res := range r.Results {
		if !res.Passed {
			return false
		}
	}
	return true
}

// Failures returns failed case names.
func (r Report) Failures() []string {
	var out []string
	for _, res := range r.Results {
		if !res.Passed {
			out = append(out, fmt.Sprintf("%s: %s", res.Name, res.Detail))
		}
	}
	return out
}
