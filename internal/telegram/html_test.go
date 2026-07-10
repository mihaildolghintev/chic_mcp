package telegram

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRenderTelegramHTML(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain text untouched", "Привет, как дела?", "Привет, как дела?"},
		{"bold", "скидка **50%** срочно", "скидка <b>50%</b> срочно"},
		{"italic", "это *важно* сейчас", "это <i>важно</i> сейчас"},
		{"inline code", "код `АРТ-1` тут", "код <code>АРТ-1</code> тут"},
		{"heading to bold", "# 🔴 Категория 1", "<b>🔴 Категория 1</b>"},
		{"link", "смотри [сайт](https://chic.md)", `смотри <a href="https://chic.md">сайт</a>`},
		{"unsafe link becomes text", "[клик](javascript:alert(1))", "клик"},
		{"stray specials escaped", "1 < 2 & 3 > 2", "1 &lt; 2 &amp; 3 &gt; 2"},
		{"raw html shown as text", "<script>alert(1)</script>", "&lt;script&gt;alert(1)&lt;/script&gt;"},
		{"soft break kept as newline", "остаток: 12\nцена: 500", "остаток: 12\nцена: 500"},
		{"paragraphs separated", "итог\n\nдетали", "итог\n\nдетали"},
		{"card layout", "📦 **Rimel 9ml**\nостаток: 12", "📦 <b>Rimel 9ml</b>\nостаток: 12"},
		{"bullet list", "- раз\n- два", "• раз\n• два"},
		{"ordered list", "1. раз\n2. два", "1. раз\n2. два"},
		{"hr dropped", "итог\n\n---\n\nдетали", "итог\n\nдетали"},
		{"table to lines", "| Товар | Остаток |\n|---|---|\n| Rimel | 12 |", "Товар — Остаток\nRimel — 12"},
		{"short blockquote plain", "> заметка", "<blockquote>заметка</blockquote>"},
		{"code fence with language", "```go\nx := 1\n```", `<pre><code class="language-go">x := 1</code></pre>`},
		{"code fence escapes body", "```\na < b & c\n```", "<pre>a &lt; b &amp; c</pre>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := renderTelegramHTML(tc.in); got != tc.want {
				t.Errorf("renderTelegramHTML(%q)\n got %q\nwant %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestRenderExpandableBlockquote checks a long quote collapses.
func TestRenderExpandableBlockquote(t *testing.T) {
	var lines []string
	for i := 0; i < expandableLines+1; i++ {
		lines = append(lines, "> строка")
	}
	got := renderTelegramHTML(strings.Join(lines, "\n"))
	if !strings.HasPrefix(got, "<blockquote expandable>") {
		t.Errorf("long quote must be expandable, got %q", got)
	}
}

func TestStripTags(t *testing.T) {
	in := `📦 <b>Товар</b>: 1 &lt; 2 &amp; <a href="https://x">ссылка</a>`
	want := "📦 Товар: 1 < 2 & ссылка"
	if got := stripTags(in); got != want {
		t.Errorf("stripTags = %q, want %q", got, want)
	}
}

func TestSplitHTML_ShortPassesThrough(t *testing.T) {
	got := splitHTML("<b>всё</b>", 4096)
	if len(got) != 1 || got[0] != "<b>всё</b>" {
		t.Errorf("got %v", got)
	}
}

// TestSplitHTML_BalancesTagsAcrossChunks cuts inside an open <blockquote
// expandable> and checks every chunk parses on its own: the tag is closed at
// the cut and reopened (with its attribute) in the next chunk.
func TestSplitHTML_BalancesTagsAcrossChunks(t *testing.T) {
	text := "<b>Итог</b>\n<blockquote expandable>" + strings.Repeat("строка списка\n", 40) + "конец</blockquote>"
	got := splitHTML(text, 300)
	if len(got) < 2 {
		t.Fatalf("expected a split, got %d chunks", len(got))
	}
	for i, chunk := range got {
		if n := utf8.RuneCountInString(chunk); n > 300 {
			t.Errorf("chunk %d is %d runes, over the limit", i, n)
		}
		var open []openTag
		if open = scanTags(open, chunk); len(open) != 0 {
			t.Errorf("chunk %d leaves %d tags open: %q", i, len(open), chunk)
		}
	}
	if !strings.HasSuffix(got[0], "</blockquote>") {
		t.Errorf("chunk 0 must close the blockquote, got tail %q", got[0][len(got[0])-30:])
	}
	if !strings.HasPrefix(got[1], "<blockquote expandable>") {
		t.Errorf("chunk 1 must reopen the blockquote with attribute, got head %q", got[1][:30])
	}
}
