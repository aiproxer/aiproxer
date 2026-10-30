package conversationprojection

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckedCapacity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		parts   []int
		want    int
		wantErr bool
	}{
		{name: "sum", parts: []int{1, 2, 3}, want: 6},
		{name: "maximum boundary", parts: []int{math.MaxInt - 1, 1}, want: math.MaxInt},
		{name: "maximum with zero", parts: []int{math.MaxInt, 0}, want: math.MaxInt},
		{name: "two part overflow", parts: []int{math.MaxInt, 1}, wantErr: true},
		{name: "three part overflow", parts: []int{math.MaxInt - 1, 1, 1}, wantErr: true},
		{name: "negative part", parts: []int{1, -1}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := checkedCapacity(tc.parts...)
			if tc.wantErr {
				assert.ErrorIs(t, err, ErrProjectionFailed)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
