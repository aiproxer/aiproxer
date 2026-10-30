//go:build configsource_faulttest

package configsource

import (
	"context"
	"os"
)

// LoadCloseFaultBaseline is available only to the mandatory lifecycle fault
// test lane. It injects the closer on the accepted pin after the normal read
// and target validation have completed, without changing runtime APIs.
func LoadCloseFaultBaseline(path string, closer func(*os.File) error) (*ActiveSourceVersion, *SourceOwnerSlot, *FixedSource, error) {
	source, err := NewFixedSource(path, 0)
	if err != nil {
		return nil, nil, nil, err
	}
	snapshot, _, err := source.ReadStable(context.Background(), nil)
	if err != nil {
		return nil, nil, nil, err
	}
	version, slot := snapshot.TakeBaseline()
	if owner := slot.owner.Load(); owner != nil {
		owner.core.closeFile = closer
	}
	return version, slot, source, nil
}
