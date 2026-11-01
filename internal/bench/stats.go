package bench

import (
	"math"
	"slices"
	"time"
)

// Percentile returns the q-quantile of ds by the nearest-rank method: the
// smallest observation that at least q of the observations do not exceed,
// the ⌈q·n⌉-th of n in ascending order (the first for q = 0). So the p50
// of 20 observations is the 10th and the p95 the 19th, and the p50 of 2 is
// the 1st. It does not change ds; it returns 0 for no observations.
func Percentile(ds []time.Duration, q float64) time.Duration {
	n := len(ds)
	if n == 0 {
		return 0
	}
	s := slices.Clone(ds)
	slices.Sort(s)
	return s[rank(n, q)]
}

// rank is the zero-based index of the nearest-rank q-quantile of n sorted
// observations.
func rank(n int, q float64) int {
	// q·n is rounded first, so that a product that is an integer in exact
	// arithmetic (0.95·20 = 19) is not taken one rank up by a
	// floating-point error.
	k := int(math.Ceil(math.Round(q*float64(n)*1e9)/1e9)) - 1
	return min(max(k, 0), n-1)
}

// Mean returns the arithmetic mean of ds, or 0 for no observations.
func Mean(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	var sum time.Duration
	for _, d := range ds {
		sum += d
	}
	return sum / time.Duration(len(ds))
}
