// Package aggregate compresses raw MoySklad API responses into compact,
// LLM-friendly structures. A hard rule lives here: MoySklad stores every
// monetary amount in kopecks, so all conversion to rubles happens in this
// layer and nowhere else.
package aggregate

import "math"

// KopecksToRubles converts an integer-kopeck amount (as MoySklad returns it,
// typed float64 in JSON) to rubles, rounded to 2 decimal places.
func KopecksToRubles(kopecks float64) float64 {
	return math.Round(kopecks) / 100.0
}

// round2 rounds to two decimal places.
func round2(v float64) float64 {
	return math.Round(v*100) / 100
}
