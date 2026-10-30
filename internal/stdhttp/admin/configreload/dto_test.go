package configreload_test

import (
	"testing"

	mgmtreload "github.com/aiproxer/aiproxer/internal/stdhttp/admin/configreload"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
)

func TestReloadAPI_HTTPStatusGoldens(t *testing.T) {
	t.Parallel()
	cases := []struct {
		cat  sdkreload.ResultCategory
		want int
	}{
		{sdkreload.ResultPublished, 200},
		{sdkreload.ResultNoop, 200},
		{sdkreload.ResultBusy, 409},
		{sdkreload.ResultRestartRequired, 409},
		{sdkreload.ResultRetentionBlocked, 409},
		{sdkreload.ResultInvalid, 422},
		{sdkreload.ResultSourceIntegrity, 422},
		{sdkreload.ResultCanceled, 503},
		{sdkreload.ResultPreparationFailed, 503},
		{sdkreload.ResultInternalFailed, 503},
	}
	for _, tc := range cases {
		if got := mgmtreload.HTTPStatusFor(tc.cat); got != tc.want {
			t.Fatalf("%s => %d want %d", tc.cat, got, tc.want)
		}
	}
}
