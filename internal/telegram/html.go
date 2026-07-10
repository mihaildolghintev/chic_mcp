// Telegram-HTML message plumbing: splitting a rendered message across the
// 4096-char limit and the plain-text fallback. The HTML itself is produced by
// renderTelegramHTML (render.go), which guarantees balanced, whitelisted tags —
// this file only moves that output around.
package telegram

import (
	"html"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// anyTagRe rescans tags in already-rendered text (splitting, stripping).
var anyTagRe = regexp.MustCompile(`<(/?)([a-z-]+)(?: [^<>]*)?>`)

// openTag remembers an open tag: the literal token (to reopen it in the next
// message chunk) and the bare name (to synthesize its closing tag).
type openTag struct {
	token, name string
}

func lastOpen(stack []openTag, name string) int {
	for j := len(stack) - 1; j >= 0; j-- {
		if stack[j].name == name {
			return j
		}
	}
	return -1
}

// stripTags is the plain-text fallback: it removes the whitelisted tags and
// unescapes entities, for a resend without ParseMode.
func stripTags(s string) string {
	return html.UnescapeString(anyTagRe.ReplaceAllString(s, ""))
}

// tagReserve is headroom splitHTML keeps under the Telegram limit for the
// closing/reopening tags it adds around a chunk boundary.
const tagReserve = 128

// splitHTML splits rendered HTML into chunks that each parse on their own:
// tags open at a cut are closed at the chunk's end and reopened at the start
// of the next one. Input must come from renderTelegramHTML (balanced,
// whitelisted tags), which also makes a cut inside a tag token practically
// impossible — it would take thousands of runes without a newline.
func splitHTML(text string, limit int) []string {
	if utf8.RuneCountInString(text) <= limit {
		return []string{text}
	}
	chunks := splitMessage(text, limit-tagReserve)
	var open []openTag
	for i, c := range chunks {
		var pre strings.Builder
		for _, t := range open {
			pre.WriteString(t.token)
		}
		open = scanTags(open, c)
		var post strings.Builder
		for _, o := range slices.Backward(open) {
			post.WriteString("</")
			post.WriteString(o.name)
			post.WriteString(">")
		}
		chunks[i] = pre.String() + c + post.String()
	}
	return chunks
}

// scanTags advances the open-tag stack over one chunk of rendered HTML.
func scanTags(stack []openTag, chunk string) []openTag {
	for _, m := range anyTagRe.FindAllStringSubmatch(chunk, -1) {
		if m[1] == "/" {
			if idx := lastOpen(stack, m[2]); idx >= 0 {
				stack = stack[:idx]
			}
			continue
		}
		stack = append(stack, openTag{token: m[0], name: m[2]})
	}
	return stack
}
