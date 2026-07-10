// Package aggregate compresses raw MoySklad API responses into compact,
// LLM-friendly structures. A hard rule lives here: MoySklad stores every
// monetary amount in the account currency's minor units (1/100 of the major
// unit, whatever the currency), so all conversion to major units happens in
// this layer and nowhere else. The layer is currency-agnostic — the currency
// label is resolved separately (moysklad.AccountCurrency) and never assumed.
package aggregate

import "math"

// MinorToMajor converts an integer minor-unit amount (as MoySklad returns it,
// typed float64 in JSON) to major units, rounded to 2 decimal places. Every
// MoySklad currency uses 1/100 minor units, so this holds for RUB, MDL, EUR
// and the rest alike.
func MinorToMajor(minor float64) float64 {
	return math.Round(minor) / 100.0
}

// round2 rounds to two decimal places.
func round2(v float64) float64 {
	return math.Round(v*100) / 100
}
