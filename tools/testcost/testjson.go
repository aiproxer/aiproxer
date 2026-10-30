package testcost

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"runtime"
)

type TestEvent struct {
	Action  string  `json:"Action"`
	Package string  `json:"Package"`
	Test    string  `json:"Test"`
	Output  string  `json:"Output"`
	Elapsed float64 `json:"Elapsed"`
}

// ParseTestJSON extracts package-level pass events. It intentionally does not
// derive process wall time from package elapsed values.
func ParseTestJSON(input io.Reader, target ...string) (Measurement, error) {
	if len(target) != 1 || target[0] == "" {
		return Measurement{}, ErrMissingTarget
	}
	measurement := Measurement{
		SchemaVersion: SchemaVersion,
		Target:        target[0],
		GOOS:          runtime.GOOS,
		GOARCH:        runtime.GOARCH,
		GoVersion:     runtime.Version(),
		LogicalCPUs:   runtime.NumCPU(),
		Packages:      make(map[string]PackageMetrics),
	}
	dec := json.NewDecoder(input)
	for {
		var event TestEvent
		if err := dec.Decode(&event); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return Measurement{}, fmt.Errorf("%w: %v", ErrMalformedJSON, err)
		}
		if event.Action != "pass" || event.Test != "" {
			continue
		}
		if event.Package == "" {
			return Measurement{}, fmt.Errorf("%w: package pass has no package", ErrMalformedJSON)
		}
		if math.IsNaN(event.Elapsed) || math.IsInf(event.Elapsed, 0) || event.Elapsed < 0 {
			return Measurement{}, fmt.Errorf("%w: invalid elapsed", ErrMalformedJSON)
		}
		measurement.Packages[event.Package] = PackageMetrics{ElapsedNanos: secondsToNanosFloat(event.Elapsed)}
	}
	return measurement, nil
}

func ParseTest2JSON(input io.Reader, target ...string) (Measurement, error) {
	return ParseTestJSON(input, target...)
}
