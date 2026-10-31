package charges

// SetRangeLimitForTest lowers ListRange's row ceiling and returns a restore func.
func SetRangeLimitForTest(n int) func() {
	old := maxRangeRows
	maxRangeRows = n
	return func() { maxRangeRows = old }
}
