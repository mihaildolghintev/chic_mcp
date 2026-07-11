package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"mcp.chic.md/internal/llm"
	"mcp.chic.md/internal/store"
)

// seedHistory appends n alternating-role turns for userID, each big enough that
// n of them overflow the summary budget. Every turn is tagged "turn-<i>" so a
// test can tell which survived verbatim and which were folded into the summary.
func seedHistory(t *testing.T, st *store.DB, userID int64, n int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		content := fmt.Sprintf("turn-%d %s", i, strings.Repeat("x", 1000))
		if err := st.AppendMessage(ctx, userID, role, content); err != nil {
			t.Fatalf("seed turn %d: %v", i, err)
		}
	}
}

// TestHandle_SummarizesOverflowingHistory: a history past the char budget is
// condensed — the older turns go to a summarization call, and the main request
// carries a summary system message plus only the most recent turns verbatim.
func TestHandle_SummarizesOverflowingHistory(t *testing.T) {
	script := &scriptedLLM{responses: []string{
		final("СВОДКА: пользователь спрашивал про продажи.", 30), // the condense call
		final("Итоговый ответ.", 20),                             // the real answer
	}}
	a, st := newTestAgent(t, script, &fakeAPI{}, Options{})
	seedHistory(t, st, 7, 10) // 10 turns × ~1KB ≫ 8000-char budget

	res, err := a.Handle(context.Background(), 7, "новый вопрос", "")
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if res.Text != "Итоговый ответ." {
		t.Errorf("answer = %q", res.Text)
	}
	if len(script.requests) != 2 {
		t.Fatalf("want a summarize call then an answer call, got %d requests", len(script.requests))
	}

	// Request 0 is the summarization: it sees the OLD turns (turn-0..turn-3).
	sumMsgs := script.requests[0]["messages"].([]any)
	transcript := sumMsgs[len(sumMsgs)-1].(map[string]any)["content"].(string)
	if !strings.Contains(transcript, "turn-0") || !strings.Contains(transcript, "turn-3") {
		t.Errorf("summarize call missing old turns:\n%s", transcript)
	}
	if strings.Contains(transcript, "turn-9") {
		t.Errorf("summarize call must not include the most recent turns")
	}

	// Request 1 is the answer: a system summary message, the recent turns
	// verbatim (turn-4..turn-9), and none of the summarized older turns.
	mainMsgs := script.requests[1]["messages"].([]any)
	var haveSummary, haveRecent, haveOld bool
	for _, m := range mainMsgs {
		content, _ := m.(map[string]any)["content"].(string)
		if strings.Contains(content, "Краткое содержание") && strings.Contains(content, "СВОДКА") {
			haveSummary = true
		}
		if strings.Contains(content, "turn-9") {
			haveRecent = true
		}
		if strings.Contains(content, "turn-0") {
			haveOld = true
		}
	}
	if !haveSummary {
		t.Error("main request missing the injected summary system message")
	}
	if !haveRecent {
		t.Error("main request missing the recent verbatim turns")
	}
	if haveOld {
		t.Error("main request still replays a summarized old turn verbatim")
	}
}

// TestHandle_ShortHistoryNotSummarized: a history under budget is replayed as-is
// with no extra summarization call.
func TestHandle_ShortHistoryNotSummarized(t *testing.T) {
	script := &scriptedLLM{responses: []string{final("ответ", 10)}}
	a, st := newTestAgent(t, script, &fakeAPI{}, Options{})
	seedHistory(t, st, 7, 2) // 2 small turns, well under budget

	if _, err := a.Handle(context.Background(), 7, "вопрос", ""); err != nil {
		t.Fatal(err)
	}
	if len(script.requests) != 1 {
		t.Fatalf("short history triggered an extra call: %d requests", len(script.requests))
	}
}

// TestMaybeConsolidate_MergesProfile: remembering a preference past the
// threshold triggers a consolidation pass that reconciles the stored profile to
// the model's canonical set — extra keys deleted, merged values written.
func TestMaybeConsolidate_MergesProfile(t *testing.T) {
	canonical := map[string]any{
		"preferences": []map[string]string{
			{"key": "language", "value": "английский"},
			{"key": "reply_style", "value": "кратко"},
			{"key": "main_warehouse", "value": "Основной склад"},
		},
	}
	raw, _ := json.Marshal(canonical)
	script := &scriptedLLM{responses: []string{final(string(raw), 40)}}
	a, st := newTestAgent(t, script, &fakeAPI{}, Options{})
	ctx := context.Background()

	// Pre-seed 7 preferences with duplicates/synonyms; the 8th is remembered
	// below and pushes the profile to the consolidation threshold.
	for i, kv := range [][2]string{
		{"lang", "англ"}, {"language", "english"}, {"reply_style", "коротко"},
		{"style", "кратко"}, {"warehouse", "склад 1"}, {"main_warehouse", "основной"},
		{"tone", "деловой"},
	} {
		if err := st.SetPreference(ctx, 7, kv[0], kv[1]); err != nil {
			t.Fatalf("seed pref %d: %v", i, err)
		}
	}

	res := a.callMemoryTool(ctx, 7, llm.ToolCall{
		Function: llm.FunctionCall{
			Name:      toolRememberPreference,
			Arguments: `{"key":"currency","value":"MDL"}`,
		},
	})
	if strings.HasPrefix(res, "ERROR") {
		t.Fatalf("remember failed: %q", res)
	}

	// The consolidation call ran and reconciled the profile down to the
	// canonical three.
	if len(script.requests) != 1 {
		t.Fatalf("want exactly one consolidation call, got %d", len(script.requests))
	}
	prefs, err := st.Preferences(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, p := range prefs {
		got[p.Key] = p.Value
	}
	want := map[string]string{
		"language": "английский", "reply_style": "кратко", "main_warehouse": "Основной склад",
	}
	if len(got) != len(want) {
		t.Fatalf("consolidated profile = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("pref %q = %q, want %q", k, got[k], v)
		}
	}
}

// TestMaybeConsolidate_SkipsBelowThreshold: a small profile is never sent for
// consolidation — remembering there makes no LLM call.
func TestMaybeConsolidate_SkipsBelowThreshold(t *testing.T) {
	script := &scriptedLLM{} // no responses scripted → any LLM call fails the test
	a, st := newTestAgent(t, script, &fakeAPI{}, Options{})
	ctx := context.Background()

	res := a.callMemoryTool(ctx, 7, llm.ToolCall{
		Function: llm.FunctionCall{Name: toolRememberPreference, Arguments: `{"key":"language","value":"en"}`},
	})
	if strings.HasPrefix(res, "ERROR") {
		t.Fatalf("remember failed: %q", res)
	}
	if len(script.requests) != 0 {
		t.Errorf("consolidation ran below threshold: %d LLM calls", len(script.requests))
	}
	if prefs, _ := st.Preferences(ctx, 7); len(prefs) != 1 {
		t.Errorf("profile = %+v, want the single remembered pref", prefs)
	}
}

// TestParseConsolidated covers the guards: prose-wrapped JSON is tolerated,
// over-long or empty entries are dropped, duplicates collapse, and junk is
// rejected outright so a bad reply can never wipe the profile.
func TestParseConsolidated(t *testing.T) {
	// Prose + code fence around valid JSON is tolerated.
	fenced := "Вот результат:\n```json\n{\"preferences\":[{\"key\":\"language\",\"value\":\"en\"}]}\n```"
	got, ok := parseConsolidated(fenced)
	if !ok || len(got) != 1 || got[0].Key != "language" || got[0].Value != "en" {
		t.Fatalf("fenced parse = %+v ok=%v", got, ok)
	}

	// Over-long value and empty key are dropped; duplicate keys collapse.
	long := strings.Repeat("я", maxPreferenceValueLen+1)
	dirty := fmt.Sprintf(`{"preferences":[
		{"key":"a","value":"1"},
		{"key":"a","value":"2"},
		{"key":"","value":"x"},
		{"key":"b","value":%q}
	]}`, long)
	got, ok = parseConsolidated(dirty)
	if !ok {
		t.Fatal("dirty-but-parseable input rejected")
	}
	if len(got) != 1 || got[0].Key != "a" || got[0].Value != "1" {
		t.Errorf("dirty parse = %+v, want only the first valid a=1", got)
	}

	// No JSON at all → rejected, not an empty success.
	if _, ok := parseConsolidated("извини, не понял"); ok {
		t.Error("non-JSON reply must be rejected")
	}
}
