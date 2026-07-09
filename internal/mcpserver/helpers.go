package mcpserver

import (
	"github.com/mark3labs/mcp-go/mcp"

	"mcp.chic.md/internal/aggregate"
)

// clampLimit bounds a requested row limit to [1, 1000], defaulting to 100.
func clampLimit(n int) int {
	switch {
	case n <= 0:
		return 100
	case n > 1000:
		return 1000
	default:
		return n
	}
}

// periodDays returns the number of days between two MoySklad date strings, or
// fallback when either is missing/unparseable. Used for turnover-days math.
func periodDays(from, to string, fallback float64) float64 {
	f, okf := aggregate.ParseTime(normalize(from))
	t, okt := aggregate.ParseTime(normalize(to))
	if !okf || !okt {
		return fallback
	}
	d := t.Sub(f).Hours() / 24
	if d <= 0 {
		return fallback
	}
	return d
}

// normalize turns a bare "YYYY-MM-DD" into the "YYYY-MM-DD 00:00:00" form
// aggregate.ParseTime expects.
func normalize(s string) string {
	if s == "" {
		return ""
	}
	if len(s) == 10 {
		return s + " 00:00:00"
	}
	return s
}

// dateArgs reads optional "date_from"/"date_to" string arguments.
func dateArgs(req mcp.CallToolRequest) (from, to string) {
	return req.GetString("date_from", ""), req.GetString("date_to", "")
}
