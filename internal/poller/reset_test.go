package poller

import "testing"

func TestDetectReset(t *testing.T) {
	const dropPct = 5.0
	const minAdvance = int64(3600)
	cases := []struct {
		name                string
		prevPct, curPct     float64
		prevReset, curReset int64
		want                bool
	}{
		{"first reading no prev", 0, 14, 0, 100, false},        // prevReset==0 → no prior baseline
		{"steady climb", 12, 14, 100, 100, false},              // same window, no drop
		{"resets_at advanced", 18, 19, 100, 604900, true},      // advance >> 3600 → natural reset
		{"sudden drop to zero", 18, 0, 100, 100, true},         // big drop, same window
		{"small dip below threshold", 18, 16, 100, 100, false}, // dip < 5 pct
		{"big drop same reset", 30, 6, 100, 100, true},         // drop >= 5 pct
		{"jitter up", 15, 14, 1781841599, 1781841600, false},   // +1s jitter must not fire
		{"jitter down", 15, 14, 1781841600, 1781841599, false}, // -1s jitter must not fire
	}
	for _, tc := range cases {
		got := DetectReset(tc.prevPct, tc.prevReset, tc.curPct, tc.curReset, dropPct, minAdvance)
		if got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}
