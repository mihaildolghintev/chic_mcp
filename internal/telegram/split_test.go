package telegram

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSplitMessage_ShortPassesThrough(t *testing.T) {
	got := splitMessage("привет", 4096)
	if len(got) != 1 || got[0] != "привет" {
		t.Errorf("got %v", got)
	}
}

func TestSplitMessage_BreaksOnNewline(t *testing.T) {
	text := strings.Repeat("строка\n", 30) // 210 runes
	got := splitMessage(text, 100)
	if len(got) < 2 {
		t.Fatalf("expected a split, got %d chunks", len(got))
	}
	for i, chunk := range got {
		if utf8.RuneCountInString(chunk) > 100 {
			t.Errorf("chunk %d is %d runes, over the limit", i, utf8.RuneCountInString(chunk))
		}
	}
	// Newline-preferring cut: chunks end on a whole line, not mid-word.
	if !strings.HasSuffix(got[0], "строка") {
		t.Errorf("chunk 0 ends mid-line: %q", got[0][len(got[0])-20:])
	}
	// Nothing lost.
	if strings.Join(got, "\n") != strings.TrimRight(text, "\n") {
		t.Error("splitting lost content")
	}
}

func TestSplitMessage_HardCutWithoutNewlines(t *testing.T) {
	text := strings.Repeat("ж", 250)
	got := splitMessage(text, 100)
	if len(got) != 3 {
		t.Fatalf("got %d chunks, want 3", len(got))
	}
	total := 0
	for _, c := range got {
		total += utf8.RuneCountInString(c)
	}
	if total != 250 {
		t.Errorf("runes after split = %d, want 250", total)
	}
}
