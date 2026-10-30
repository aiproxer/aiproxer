//go:build linux && configsource_faulttest

package runtimebundle

import (
	"errors"
	"os"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/configsource"
)

func TestHostCloseCachedSourceCloseFailure(t *testing.T) {
	for _, admitted := range []bool{false, true} {
		t.Run(map[bool]string{false: "initially-idle", true: "admitted-attempt"}[admitted], func(t *testing.T) {
			injected := errors.New("private source-close cause")
			var closes atomic.Int32
			load := func(path string) (*configsource.ActiveSourceVersion, *configsource.SourceOwnerSlot, *configsource.FixedSource, error) {
				return configsource.LoadCloseFaultBaseline(path, func(f *os.File) error {
					closes.Add(1)
					return errors.Join(f.Close(), injected)
				})
			}
			exerciseHostCloseSource(t, admitted, load, injected)
			if closes.Load() != 1 {
				t.Fatalf("source physical close calls=%d want once", closes.Load())
			}
		})
	}
}
