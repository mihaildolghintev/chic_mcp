package moysklad

import (
	"net/url"
	"strings"
)

// NamedRef is a nested entity reference. MoySklad returns only Meta unless the
// field is expanded; Name is populated once expanded or for report holders.
type NamedRef struct {
	Meta Meta   `json:"meta"`
	Name string `json:"name,omitempty"`
	Code string `json:"code,omitempty"`
}

// Amount is a monetary value as MoySklad returns it: an integer number of the
// account currency's minor units (1/100 of the major unit — kopecks for RUB,
// bani for MDL, cents for EUR), typed float64 by the JSON decoder. Convert to
// major units only in the aggregation layer.
type Amount = float64

// normalizeMoment converts a "YYYY-MM-DD" or RFC3339-ish date into the format
// MoySklad expects for momentFrom/momentTo/moment parameters:
// "YYYY-MM-DD HH:MM:SS". A bare date gets midnight appended. Empty stays empty.
func normalizeMoment(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Already has a time component (space- or T-separated).
	if strings.Contains(s, " ") {
		return s
	}
	if i := strings.IndexByte(s, 'T'); i >= 0 {
		datePart := s[:i]
		timePart := strings.TrimSuffix(s[i+1:], "Z")
		if len(timePart) >= 8 {
			timePart = timePart[:8]
		}
		return datePart + " " + timePart
	}
	return s + " 00:00:00"
}

// setMoment adds momentFrom/momentTo to a query if non-empty.
func setPeriod(v url.Values, from, to string) {
	if m := normalizeMoment(from); m != "" {
		v.Set("momentFrom", m)
	}
	if m := normalizeMoment(to); m != "" {
		v.Set("momentTo", m)
	}
}
