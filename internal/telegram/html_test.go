package telegram

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeHTML(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain text untouched", "Привет, как дела?", "Привет, как дела?"},
		{"allowed tags pass", "<b>итог</b> и <i>деталь</i>", "<b>итог</b> и <i>деталь</i>"},
		{"code and pre pass", "<code>АРТ-1</code> <pre>x = 1</pre>", `<code>АРТ-1</code> <pre>x = 1</pre>`},
		{"expandable blockquote passes", "<blockquote expandable>ещё 20 позиций</blockquote>", "<blockquote expandable>ещё 20 позиций</blockquote>"},
		{"link passes", `<a href="https://chic.md">сайт</a>`, `<a href="https://chic.md">сайт</a>`},
		{"language code passes", `<pre><code class="language-go">x</code></pre>`, `<pre><code class="language-go">x</code></pre>`},
		{"unknown tag escaped", "<script>alert(1)</script>", "&lt;script&gt;alert(1)&lt;/script&gt;"},
		{"table escaped", "<table><tr><td>x</td></tr></table>", "&lt;table&gt;&lt;tr&gt;&lt;td&gt;x&lt;/td&gt;&lt;/tr&gt;&lt;/table&gt;"},
		{"stray specials escaped", "1 < 2 & 3 > 2", "1 &lt; 2 &amp; 3 &gt; 2"},
		{"known entities kept", "&lt;b&gt; &amp; &#128512;", "&lt;b&gt; &amp; &#128512;"},
		{"unknown entity escaped", "фирма&nbsp;X", "фирма&amp;nbsp;X"},
		{"unclosed tag closed", "<b>жирный до конца", "<b>жирный до конца</b>"},
		{"orphan close escaped", "просто</b> текст", "просто&lt;/b&gt; текст"},
		{"overlap resolved by nesting", "<b>раз <i>два</b> три", "<b>раз <i>два</i></b> три"},
		{"bare a without href escaped", "<a>ссылка</a>", "&lt;a&gt;ссылка&lt;/a&gt;"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeHTML(tc.in); got != tc.want {
				t.Errorf("sanitizeHTML(%q)\n got %q\nwant %q", tc.in, got, tc.want)
			}
		})
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
