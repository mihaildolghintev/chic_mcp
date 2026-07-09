package aggregate

import (
	"math"
	"sort"
	"time"

	"mcp.chic.md/internal/moysklad"
)

// moyskladTimeLayout is the timestamp format MoySklad uses in JSON.
const moyskladTimeLayout = "2006-01-02 15:04:05"

// ParseTime parses a MoySklad timestamp; ok is false for empty/invalid input.
func ParseTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(moyskladTimeLayout, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// ---- ABC analysis ---------------------------------------------------------

// ABCClass is a Pareto class: A (top contributors), B (middle), C (long tail).
type ABCClass string

const (
	ClassA ABCClass = "A"
	ClassB ABCClass = "B"
	ClassC ABCClass = "C"
)

// ABCItem carries an item, its metric value, and its Pareto classification.
type ABCItem[T any] struct {
	Item     T        `json:"item"`
	Value    float64  `json:"value"`
	Share    float64  `json:"share"`           // this item's share of the total, %
	Cumshare float64  `json:"cumulativeShare"` // running cumulative share, %
	Class    ABCClass `json:"class"`
}

// ABC ranks items by value descending and assigns A/B/C by cumulative share.
// aCut/bCut are the cumulative-share cutoffs (e.g. 0.8 and 0.95); items up to
// aCut are A, up to bCut are B, the rest C. Items with non-positive value are
// classed C. Zero cutoffs fall back to 0.8/0.95.
func ABC[T any](items []T, value func(T) float64, aCut, bCut float64) []ABCItem[T] {
	if aCut <= 0 || aCut >= 1 {
		aCut = 0.8
	}
	if bCut <= aCut || bCut >= 1 {
		bCut = 0.95
	}

	total := 0.0
	for _, it := range items {
		if v := value(it); v > 0 {
			total += v
		}
	}

	res := make([]ABCItem[T], len(items))
	for i, it := range items {
		res[i] = ABCItem[T]{Item: it, Value: value(it)}
	}
	sort.SliceStable(res, func(i, j int) bool { return res[i].Value > res[j].Value })

	cum := 0.0
	for i := range res {
		v := res[i].Value
		if total > 0 && v > 0 {
			res[i].Share = round2(v / total * 100)
			cum += v
			res[i].Cumshare = round2(cum / total * 100)
		} else {
			res[i].Cumshare = 100
		}
		switch {
		case v <= 0:
			res[i].Class = ClassC
		case cum/max(total, 1) <= aCut:
			res[i].Class = ClassA
		case cum/max(total, 1) <= bCut:
			res[i].Class = ClassB
		default:
			res[i].Class = ClassC
		}
	}
	return res
}

// ---- Counterparty segmentation (RFM-style rules) --------------------------

// SegmentParams configures the segmentation rules. Zero fields fall back to
// sensible defaults.
type SegmentParams struct {
	Now               time.Time
	SleepingDays      int     // no purchase for longer than this -> "sleeping" (default 90)
	AtRiskDays        int     // gap between this and SleepingDays -> "at_risk" (default 45)
	VIPTopPercent     float64 // top X by revenue share -> "vip" (default 0.2)
	LowCheckThreshold float64 // avg receipt below this (rubles) -> "low_check" (default 0 = off)
}

func (p SegmentParams) withDefaults() SegmentParams {
	if p.SleepingDays == 0 {
		p.SleepingDays = 90
	}
	if p.AtRiskDays == 0 {
		p.AtRiskDays = 45
	}
	if p.VIPTopPercent == 0 {
		p.VIPTopPercent = 0.2
	}
	if p.Now.IsZero() {
		p.Now = time.Now()
	}
	return p
}

// CounterpartySegment is a counterparty with its assigned segment labels.
type CounterpartySegment struct {
	Name                  string   `json:"name"`
	Segments              []string `json:"segments"`
	Revenue               float64  `json:"revenue"`
	AvgReceipt            float64  `json:"avgReceipt"`
	Profit                float64  `json:"profit"`
	Balance               float64  `json:"balance"`
	DaysSinceLastPurchase int      `json:"daysSinceLastPurchase"`
}

// SegmentCounterparties assigns labels: vip, sleeping, at_risk, low_check,
// debtor, negative_margin. A counterparty may carry several labels.
func SegmentCounterparties(rows []moysklad.CounterpartyRow, p SegmentParams) []CounterpartySegment {
	p = p.withDefaults()

	metrics := CounterpartyMetrics(rows)

	// Determine VIP revenue threshold: the value at the top VIPTopPercent rank.
	revenues := make([]float64, 0, len(metrics))
	for _, m := range metrics {
		revenues = append(revenues, m.Revenue)
	}
	sort.Sort(sort.Reverse(sort.Float64Slice(revenues)))
	vipThreshold := math.MaxFloat64
	if n := len(revenues); n > 0 {
		idx := int(float64(n) * p.VIPTopPercent)
		if idx >= n {
			idx = n - 1
		}
		vipThreshold = revenues[idx]
	}

	out := make([]CounterpartySegment, 0, len(metrics))
	for _, m := range metrics {
		seg := CounterpartySegment{
			Name:       m.Name,
			Revenue:    m.Revenue,
			AvgReceipt: m.AvgReceipt,
			Profit:     m.Profit,
			Balance:    m.Balance,
		}

		days := -1
		if last, ok := ParseTime(m.LastDemand); ok {
			days = int(p.Now.Sub(last).Hours() / 24)
		}
		seg.DaysSinceLastPurchase = days

		if m.Revenue > 0 && m.Revenue >= vipThreshold {
			seg.Segments = append(seg.Segments, "vip")
		}
		switch {
		case days < 0:
			// never purchased; leave unlabelled by recency
		case days >= p.SleepingDays:
			seg.Segments = append(seg.Segments, "sleeping")
		case days >= p.AtRiskDays:
			seg.Segments = append(seg.Segments, "at_risk")
		}
		if p.LowCheckThreshold > 0 && m.DemandsCount > 0 && m.AvgReceipt < p.LowCheckThreshold {
			seg.Segments = append(seg.Segments, "low_check")
		}
		if m.Balance > 0 {
			seg.Segments = append(seg.Segments, "debtor")
		}
		if m.Revenue > 0 && m.Profit < 0 {
			seg.Segments = append(seg.Segments, "negative_margin")
		}
		out = append(out, seg)
	}
	return out
}

// ---- Dead stock -----------------------------------------------------------

// DeadStockLine is a stock line flagged as dead: on hand for at least the
// threshold age with little or no outbound movement in the period.
type DeadStockLine struct {
	StockLine
	OutcomeQty float64 `json:"outcomeQty"`
}

// DeadStock returns stock rows with stock > 0 and stockDays >= thresholdDays.
// If outcomeByRef is non-nil (keyed by the product's meta href from the
// turnover report), an item is dead only when its outbound quantity is ~0.
func DeadStock(rows []moysklad.StockRow, outcomeByRef map[string]float64, thresholdDays int) []DeadStockLine {
	var out []DeadStockLine
	for _, r := range rows {
		if r.Stock <= 0 || r.StockDays < thresholdDays {
			continue
		}
		outcome := 0.0
		hasOutcome := false
		if outcomeByRef != nil {
			if q, ok := outcomeByRef[r.Meta.Href]; ok {
				outcome = q
				hasOutcome = true
			}
		}
		if hasOutcome && outcome > 0 {
			continue // it did move -> not dead
		}
		line := Stock([]moysklad.StockRow{r})[0]
		out = append(out, DeadStockLine{StockLine: line, OutcomeQty: outcome})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StockValue > out[j].StockValue })
	return out
}

// ---- Period comparison ----------------------------------------------------

// Change is a per-key delta between two periods.
type Change struct {
	Key      string  `json:"key"`
	ValueA   float64 `json:"valueA"`
	ValueB   float64 `json:"valueB"`
	Delta    float64 `json:"delta"`
	DeltaPct float64 `json:"deltaPct"`
}

// Comparison is the result of comparing two periods on one metric.
type Comparison struct {
	TotalA    float64  `json:"totalA"`
	TotalB    float64  `json:"totalB"`
	Delta     float64  `json:"delta"`
	DeltaPct  float64  `json:"deltaPct"`
	Gainers   []Change `json:"topGainers"`
	Decliners []Change `json:"topDecliners"`
}

// ComparePeriods compares two slices on a keyed metric, returning totals plus
// the topN keys that grew and declined the most (by absolute delta). This is
// what explains "why did revenue move" — the biggest contributors.
func ComparePeriods[T any](a, b []T, key func(T) string, value func(T) float64, topN int) Comparison {
	ma := foldByKey(a, key, value)
	mb := foldByKey(b, key, value)

	seen := make(map[string]bool)
	var changes []Change
	var totalA, totalB float64
	for k, va := range ma {
		seen[k] = true
		vb := mb[k]
		totalA += va
		changes = append(changes, mkChange(k, va, vb))
	}
	for k, vb := range mb {
		totalB += vb
		if seen[k] {
			continue
		}
		changes = append(changes, mkChange(k, 0, vb))
	}

	gainers := append([]Change(nil), changes...)
	sort.SliceStable(gainers, func(i, j int) bool { return gainers[i].Delta > gainers[j].Delta })
	decliners := append([]Change(nil), changes...)
	sort.SliceStable(decliners, func(i, j int) bool { return decliners[i].Delta < decliners[j].Delta })

	c := Comparison{
		TotalA:   round2(totalA),
		TotalB:   round2(totalB),
		Delta:    round2(totalB - totalA),
		DeltaPct: pctChange(totalA, totalB),
	}
	c.Gainers = topPositive(gainers, topN)
	c.Decliners = topNegative(decliners, topN)
	return c
}

func foldByKey[T any](items []T, key func(T) string, value func(T) float64) map[string]float64 {
	m := make(map[string]float64, len(items))
	for _, it := range items {
		m[key(it)] += value(it)
	}
	return m
}

func mkChange(k string, a, b float64) Change {
	return Change{Key: k, ValueA: round2(a), ValueB: round2(b), Delta: round2(b - a), DeltaPct: pctChange(a, b)}
}

func topPositive(sorted []Change, n int) []Change {
	var out []Change
	for _, c := range sorted {
		if c.Delta <= 0 || len(out) >= n {
			break
		}
		out = append(out, c)
	}
	return out
}

func topNegative(sorted []Change, n int) []Change {
	var out []Change
	for _, c := range sorted {
		if c.Delta >= 0 || len(out) >= n {
			break
		}
		out = append(out, c)
	}
	return out
}

func pctChange(a, b float64) float64 {
	if a == 0 {
		if b == 0 {
			return 0
		}
		return 100
	}
	return round2((b - a) / a * 100)
}

// ---- Receivables aging ----------------------------------------------------

type AgingItem struct {
	Document     string  `json:"document"`
	Counterparty string  `json:"counterparty"`
	DueDate      string  `json:"dueDate,omitempty"`
	Outstanding  float64 `json:"outstanding"`
	DaysOverdue  int     `json:"daysOverdue"`
}

type AgingBucket struct {
	Label  string  `json:"label"`
	Count  int     `json:"count"`
	Amount float64 `json:"amount"`
}

type Aging struct {
	TotalOutstanding float64       `json:"totalOutstanding"`
	TotalOverdue     float64       `json:"totalOverdue"`
	Buckets          []AgingBucket `json:"buckets"`
	Items            []AgingItem   `json:"items"`
}

// ReceivablesAging computes overdue accounts-receivable from customer invoices.
// Outstanding = sum - payedSum. An item is overdue when its payment-planned
// date is before now and something is still owed. Buckets: current, 1-30,
// 31-60, 61-90, 90+.
func ReceivablesAging(docs []moysklad.Document, now time.Time) Aging {
	buckets := []AgingBucket{
		{Label: "current"}, {Label: "1-30"}, {Label: "31-60"}, {Label: "61-90"}, {Label: "90+"},
	}
	ag := Aging{}
	for _, d := range docs {
		outstanding := KopecksToRubles(d.Sum - d.PayedSum)
		if outstanding <= 0 {
			continue
		}
		ag.TotalOutstanding += outstanding

		name := ""
		if d.Agent != nil {
			name = d.Agent.Name
		}
		item := AgingItem{
			Document:     d.Name,
			Counterparty: name,
			DueDate:      d.PaymentPlannedMoment,
			Outstanding:  outstanding,
		}

		due, ok := ParseTime(d.PaymentPlannedMoment)
		overdueDays := 0
		if ok && due.Before(now) {
			overdueDays = int(now.Sub(due).Hours() / 24)
		}
		item.DaysOverdue = overdueDays

		bi := bucketIndex(overdueDays)
		buckets[bi].Count++
		buckets[bi].Amount = round2(buckets[bi].Amount + outstanding)
		if overdueDays > 0 {
			ag.TotalOverdue += outstanding
		}
		ag.Items = append(ag.Items, item)
	}
	ag.TotalOutstanding = round2(ag.TotalOutstanding)
	ag.TotalOverdue = round2(ag.TotalOverdue)
	ag.Buckets = buckets
	sort.SliceStable(ag.Items, func(i, j int) bool { return ag.Items[i].DaysOverdue > ag.Items[j].DaysOverdue })
	return ag
}

func bucketIndex(daysOverdue int) int {
	switch {
	case daysOverdue <= 0:
		return 0
	case daysOverdue <= 30:
		return 1
	case daysOverdue <= 60:
		return 2
	case daysOverdue <= 90:
		return 3
	default:
		return 4
	}
}
