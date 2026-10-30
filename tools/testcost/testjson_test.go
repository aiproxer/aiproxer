package testcost

import (
	"errors"
	"strings"
	"testing"
)

func TestParseTestJSONUsesPackagePassAndNeverSumsWall(t *testing.T) {
	t.Parallel()

	measurement, err := ParseTestJSON(strings.NewReader(`{"Action":"pass","Package":"example/pkg","Test":"TestOne","Elapsed":9}
{"Action":"pass","Package":"example/pkg","Elapsed":1.25}
`), TargetTestUnit)
	if err != nil {
		t.Fatalf("ParseTestJSON() error = %v", err)
	}
	if got := measurement.Packages["example/pkg"].ElapsedNanos; got != 1_250_000_000 {
		t.Fatalf("package elapsed = %d, want 1250000000", got)
	}
	if measurement.WallNanos != 0 {
		t.Fatalf("parser summed package elapsed into wall_nanos: %d", measurement.WallNanos)
	}
	if _, err := ParseTestJSON(strings.NewReader(`{"Action":"pass","Package":"example/pkg","Elapsed":1}`)); !errors.Is(err, ErrMissingTarget) {
		t.Fatalf("missing parser target error = %v", err)
	}
}

func TestParseTestJSONHandlesLargeOutputEvent(t *testing.T) {
	t.Parallel()

	// Create a JSON event with > 2MB Output field followed by a package pass event
	largeString := strings.Repeat("x", 2*1024*1024)
	input := `{"Action":"output","Package":"example/pkg","Test":"TestLarge","Output":"` + largeString + `"}
{"Action":"pass","Package":"example/pkg","Elapsed":2.5}
`
	measurement, err := ParseTestJSON(strings.NewReader(input), TargetTestUnit)
	if err != nil {
		t.Fatalf("ParseTestJSON() large event error = %v", err)
	}
	if got := measurement.Packages["example/pkg"].ElapsedNanos; got != 2_500_000_000 {
		t.Fatalf("package elapsed = %d, want 2500000000", got)
	}
}
