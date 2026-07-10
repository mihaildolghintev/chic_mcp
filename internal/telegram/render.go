// Markdown -> Telegram-HTML rendering. The agent is prompted to answer in
// plain Markdown (the format LLMs emit most reliably); this file parses that
// Markdown with goldmark and walks the AST, emitting only the tags Telegram's
// HTML mode understands. Because every literal is escaped and every tag is
// opened and closed by the same recursion, the output is valid Telegram-HTML
// by construction — no separate sanitizing pass is needed.
// See https://core.telegram.org/bots/api#html-style for the tag whitelist.
package telegram

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

// md parses GitHub-flavored Markdown (tables, strikethrough, autolinks,
// task lists). We use only its parser; rendering is our own AST walk.
var md = goldmark.New(goldmark.WithExtensions(extension.GFM))

// expandableLines is the blockquote length (in lines) above which we render
// <blockquote expandable> so a long tail collapses instead of flooding the
// chat. Shorter quotes stay as a plain blockquote.
const expandableLines = 8

// renderTelegramHTML turns a Markdown answer into Telegram-HTML.
func renderTelegramHTML(src string) string {
	source := []byte(src)
	r := &tgRenderer{src: source}
	return strings.Trim(r.render(md.Parser().Parse(text.NewReader(source))), "\n")
}

type tgRenderer struct{ src []byte }

// render dispatches one node to its Telegram-HTML form.
func (r *tgRenderer) render(n ast.Node) string {
	switch n := n.(type) {
	case *ast.Document:
		return r.blockChildren(n, "\n\n")
	case *ast.Heading:
		return "<b>" + r.inlineChildren(n) + "</b>"
	case *ast.Paragraph, *ast.TextBlock:
		return r.inlineChildren(n)
	case *ast.Blockquote:
		return r.blockquote(n)
	case *ast.List:
		return r.list(n)
	case *ast.ListItem:
		return r.blockChildren(n, "\n")
	case *ast.FencedCodeBlock:
		return r.codeBlock(n, string(n.Language(r.src)))
	case *ast.CodeBlock:
		return r.codeBlock(n, "")
	case *ast.ThematicBreak:
		return "" // Telegram has no <hr>; drop it.
	case *ast.HTMLBlock:
		return escText(n.Lines().Value(r.src)) // show raw HTML as literal text
	case *east.Table:
		return r.table(n)

	case *ast.Text:
		s := escText(n.Segment.Value(r.src))
		if n.SoftLineBreak() || n.HardLineBreak() {
			s += "\n"
		}
		return s
	case *ast.String:
		return escText(n.Value)
	case *ast.Emphasis:
		if n.Level == 1 {
			return "<i>" + r.inlineChildren(n) + "</i>"
		}
		return "<b>" + r.inlineChildren(n) + "</b>"
	case *east.Strikethrough:
		return "<s>" + r.inlineChildren(n) + "</s>"
	case *ast.CodeSpan:
		return "<code>" + escText(n.Text(r.src)) + "</code>"
	case *ast.Link:
		return r.link(string(n.Destination), r.inlineChildren(n))
	case *ast.AutoLink:
		url := string(n.URL(r.src))
		return r.link(url, escText([]byte(url)))
	case *ast.Image:
		return r.link(string(n.Destination), r.inlineChildren(n)) // no inline images in text
	case *east.TaskCheckBox:
		if n.IsChecked {
			return "☑ "
		}
		return "☐ "
	case *ast.RawHTML:
		return escText(n.Segments.Value(r.src)) // show raw HTML as literal text

	default:
		if n.Type() == ast.TypeBlock {
			return r.blockChildren(n, "\n\n")
		}
		return r.inlineChildren(n)
	}
}

// blockChildren renders each block child and joins the non-empty results with
// sep ("\n\n" between paragraphs, "\n" inside a list item).
func (r *tgRenderer) blockChildren(n ast.Node, sep string) string {
	var parts []string
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if s := r.render(c); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, sep)
}

// inlineChildren concatenates the rendered inline children with no separator.
func (r *tgRenderer) inlineChildren(n ast.Node) string {
	var b strings.Builder
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		b.WriteString(r.render(c))
	}
	return b.String()
}

// blockquote wraps its content; long quotes become expandable so a folded
// tail doesn't flood the chat.
func (r *tgRenderer) blockquote(n ast.Node) string {
	body := r.blockChildren(n, "\n")
	tag := "<blockquote>"
	if strings.Count(body, "\n") >= expandableLines {
		tag = "<blockquote expandable>"
	}
	return tag + body + "</blockquote>"
}

// list renders a bullet or ordered list. Telegram has no list tags, so each
// item becomes a plain "• "/"N. " line; wrapped and nested content is indented
// under the marker.
func (r *tgRenderer) list(l *ast.List) string {
	var lines []string
	n := l.Start
	for item := l.FirstChild(); item != nil; item = item.NextSibling() {
		marker := "• "
		if l.IsOrdered() {
			marker = strconv.Itoa(n) + ". "
			n++
		}
		content := strings.Split(r.render(item), "\n")
		for i, line := range content {
			if i == 0 {
				lines = append(lines, marker+line)
			} else {
				lines = append(lines, "  "+line)
			}
		}
	}
	return strings.Join(lines, "\n")
}

// codeBlock renders a fenced or indented code block, keeping the language
// class when present.
func (r *tgRenderer) codeBlock(n ast.Node, lang string) string {
	body := escText([]byte(strings.TrimRight(string(n.Lines().Value(r.src)), "\n")))
	if lang != "" {
		return `<pre><code class="language-` + escAttr(lang) + `">` + body + "</code></pre>"
	}
	return "<pre>" + body + "</pre>"
}

// table degrades a Markdown table into one line per row, cells joined by
// " — " — Telegram has no table layout.
func (r *tgRenderer) table(t ast.Node) string {
	var rows []string
	for row := t.FirstChild(); row != nil; row = row.NextSibling() {
		var cells []string
		for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
			cells = append(cells, strings.TrimSpace(r.inlineChildren(cell)))
		}
		rows = append(rows, strings.Join(cells, " — "))
	}
	return strings.Join(rows, "\n")
}

// link renders an <a> when the URL scheme is safe, otherwise just the text —
// so a javascript:/data: URL can never reach the client.
func (r *tgRenderer) link(url, inner string) string {
	if inner == "" {
		inner = escText([]byte(url))
	}
	if !safeURL(url) {
		return inner
	}
	return fmt.Sprintf(`<a href="%s">%s</a>`, escAttr(url), inner)
}

func safeURL(u string) bool {
	l := strings.ToLower(strings.TrimSpace(u))
	return strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "http://") ||
		strings.HasPrefix(l, "tg://") || strings.HasPrefix(l, "mailto:")
}

// escText escapes the three characters Telegram treats as HTML markup, so a
// literal from the source can never open a tag or entity. Order matters: & first.
func escText(b []byte) string {
	s := string(b)
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// escAttr escapes an attribute value (href, language class) — like escText
// plus the quote that would close the attribute.
func escAttr(s string) string {
	s = escText([]byte(s))
	return strings.ReplaceAll(s, `"`, "&quot;")
}
