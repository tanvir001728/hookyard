package stats

import "github.com/tanvir001728/hookyard/internal/store"

// Percentile estimates the q-th quantile (0 < q <= 1) of a latency histogram
// given as counts per upper bound, interpolating linearly within the bucket
// that contains it, as Prometheus's histogram_quantile does. It returns nil
// for an empty histogram. Values in the overflow bucket report the largest
// finite bound.
func Percentile(counts map[int]int64, q float64) *float64 {
	var total int64
	for _, n := range counts {
		total += n
	}
	if total == 0 {
		return nil
	}

	target := q * float64(total)
	var cum int64
	lower := 0
	for _, upper := range LatencyBoundsMS {
		n := counts[upper]
		if n > 0 && float64(cum+n) >= target {
			if upper == store.LatencyInfinity {
				v := float64(lower)
				return &v
			}
			v := float64(lower) + float64(upper-lower)*(target-float64(cum))/float64(n)
			return &v
		}
		cum += n
		if upper != store.LatencyInfinity {
			lower = upper
		}
	}
	v := float64(lower)
	return &v
}
