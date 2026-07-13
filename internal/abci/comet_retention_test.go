package abci

import "testing"

func TestCometRetainHeight(t *testing.T) {
	tests := []struct {
		height int64
		retain uint64
		want   int64
	}{
		{height: 10, retain: 0, want: 0},
		{height: 3, retain: 4, want: 0},
		{height: 4, retain: 4, want: 1},
		{height: 7, retain: 4, want: 4},
	}
	for _, test := range tests {
		if got := cometRetainHeight(test.height, test.retain); got != test.want {
			t.Fatalf("height=%d retain=%d got=%d want=%d", test.height, test.retain, got, test.want)
		}
	}
}
