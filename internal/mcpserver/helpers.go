package mcpserver

import (
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"mcp.chic.md/internal/aggregate"
)

// prevMonth returns the previous full calendar month as YYYY-MM-DD from/to.
// get_turnover uses it when the caller omits dates so the period is explicit and
// periodDays lines up exactly with the data window (MoySklad's own no-date
// default is also the previous month, but we can't measure the days it picked).
func prevMonth(now time.Time) (from, to string) {
	firstThis := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	end := firstThis.AddDate(0, 0, -1)
	start := time.Date(end.Year(), end.Month(), 1, 0, 0, 0, 0, now.Location())
	return start.Format("2006-01-02"), end.Format("2006-01-02")
}

// newTool builds a tool that already carries this server's standing annotations:
// every tool is read-only (no MoySklad writes) and open-world (reaches a live
// external API). Clients use these hints to decide a call is safe to auto-run.
func newTool(name string, opts ...mcp.ToolOption) mcp.Tool {
	base := []mcp.ToolOption{
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	}
	return mcp.NewTool(name, append(base, opts...)...)
}

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
	// +1 because the range is inclusive of both endpoints: momentTo is stretched
	// to end-of-day, so 07-01..07-10 is 10 days of data, not 9.
	d := t.Sub(f).Hours()/24 + 1
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
