package poller

// DetectReset reports whether the 7d quota was reset between two readings.
//
// A reset is signalled by either:
//  1. Natural reset: the resets_at window advances by more than minAdvanceSec.
//     The threshold filters out ±1 s jitter that appears when the OAuth endpoint
//     returns a sub-second ISO timestamp and callers convert it to a unix epoch
//     (e.g. 1781841599 ↔ 1781841600 across consecutive polls even though the
//     window never moved). A value of 3600 is a safe default.
//  2. Sudden reset: utilisation drops by >= dropPct points within the same window
//     (Anthropic occasionally grants mid-week resets without advancing resets_at).
//
// prevReset==0 means there is no prior baseline; returns false.
func DetectReset(prevPct float64, prevReset int64, curPct float64, curReset int64, dropPct float64, minAdvanceSec int64) bool {
	if prevReset == 0 {
		return false
	}
	if curReset-prevReset > minAdvanceSec {
		return true
	}
	return prevPct-curPct >= dropPct
}
