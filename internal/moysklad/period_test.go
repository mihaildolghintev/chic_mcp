package moysklad

import (
	"net/url"
	"testing"
)

func TestSetPeriod_EndDateIsInclusive(t *testing.T) {
	v := url.Values{}
	setPeriod(v, "2026-07-01", "2026-07-10")
	if got := v.Get("momentFrom"); got != "2026-07-01 00:00:00" {
		t.Errorf("momentFrom = %q, want start of day", got)
	}
	// A bare end date must cover the whole final day, not stop at midnight.
	if got := v.Get("momentTo"); got != "2026-07-10 23:59:59" {
		t.Errorf("momentTo = %q, want end of day", got)
	}
}

func TestNormalizeMomentEnd_KeepsExplicitTime(t *testing.T) {
	if got := normalizeMomentEnd("2026-07-10 08:30:00"); got != "2026-07-10 08:30:00" {
		t.Errorf("explicit time changed: %q", got)
	}
}

func TestStockOptions_MomentEmitsSlice(t *testing.T) {
	v := StockOptions{StockMode: "all", Moment: "2026-07-01"}.values()
	if got := v.Get("moment"); got != "2026-07-01 00:00:00" {
		t.Errorf("moment = %q, want stock-on-date slice param", got)
	}
	// No moment -> current snapshot, param absent.
	v2 := StockOptions{StockMode: "all"}.values()
	if v2.Has("moment") {
		t.Errorf("moment param present without a date: %q", v2.Get("moment"))
	}
}
