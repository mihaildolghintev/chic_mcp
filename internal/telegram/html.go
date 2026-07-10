// Telegram-HTML sanitizing for outgoing messages. The LLM is asked to answer
// in Telegram's HTML subset, but one stray "<" or an unclosed tag makes
// sendMessage reject the whole message with 400 — so everything we send is
// normalized here first: allowed tags pass through, everything else is
// escaped, unclosed tags are closed. See
// https://core.telegram.org/bots/api#html-style for the tag whitelist.
package telegram

import (
	"html"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

var (
	// openTagRe matches an opening tag Telegram accepts, anchored at the
	// start. Groups 1-5 capture the tag name for the five attribute shapes.
	openTagRe  = regexp.MustCompile(`^<(?:(b|strong|i|em|u|ins|s|strike|del|pre|tg-spoiler)|(blockquote)(?: expandable)?|(code)(?: class="language-[^"<>]*")?|(span) class="tg-spoiler"|(a) href="[^"<>]*")>`)
	closeTagRe = regexp.MustCompile(`^</(b|strong|i|em|u|ins|s|strike|del|pre|tg-spoiler|blockquote|code|span|a)>`)
	// entityRe matches the entities Telegram understands — the four named
	// ones plus numeric; any other "&" must become &amp;.
	entityRe = regexp.MustCompile(`^&(?:lt|gt|amp|quot|#[0-9]{1,7}|#x[0-9a-fA-F]{1,6});`)
	// anyTagRe rescans tags in already-sanitized text (splitting, stripping).
	anyTagRe = regexp.MustCompile(`<(/?)([a-z-]+)(?: [^<>]*)?>`)
)

// openTag remembers an open tag: the literal token (to reopen it in the next
// message chunk) and the bare name (to synthesize its closing tag).
type openTag struct {
	token, name string
}

// sanitizeHTML makes arbitrary text safe for sendMessage with ParseMode=HTML:
// whitelisted tags pass through, any other <, > or & is escaped, orphan
// closing tags are escaped, tags left open are closed at the end (a closing
// tag also closes anything opened inside it, so overlap can't happen).
func sanitizeHTML(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	var stack []openTag
	for i := 0; i < len(s); {
		switch s[i] {
		case '<':
			if m := openTagRe.FindStringSubmatch(s[i:]); m != nil {
				name := m[1] + m[2] + m[3] + m[4] + m[5] // exactly one group is non-empty
				stack = append(stack, openTag{token: m[0], name: name})
				b.WriteString(m[0])
				i += len(m[0])
				continue
			}
			if m := closeTagRe.FindStringSubmatch(s[i:]); m != nil {
				if idx := lastOpen(stack, m[1]); idx >= 0 {
					for j := len(stack) - 1; j >= idx; j-- {
						b.WriteString("</")
						b.WriteString(stack[j].name)
						b.WriteString(">")
					}
					stack = stack[:idx]
					i += len(m[0])
					continue
				}
			}
			b.WriteString("&lt;")
			i++
		case '>':
			b.WriteString("&gt;")
			i++
		case '&':
			if m := entityRe.FindString(s[i:]); m != "" {
				b.WriteString(m)
				i += len(m)
				continue
			}
			b.WriteString("&amp;")
			i++
		default:
			b.WriteByte(s[i])
			i++
		}
	}
	for j := len(stack) - 1; j >= 0; j-- {
		b.WriteString("</")
		b.WriteString(stack[j].name)
		b.WriteString(">")
	}
	return b.String()
}

func lastOpen(stack []openTag, name string) int {
	for j := len(stack) - 1; j >= 0; j-- {
		if stack[j].name == name {
			return j
		}
	}
	return -1
}

// stripTags is the plain-text fallback: it removes the tags sanitizeHTML let
// through and unescapes entities, for a resend without ParseMode.
func stripTags(s string) string {
	return html.UnescapeString(anyTagRe.ReplaceAllString(s, ""))
}

// tagReserve is headroom splitHTML keeps under the Telegram limit for the
// closing/reopening tags it adds around a chunk boundary.
const tagReserve = 128

// splitHTML splits sanitized HTML into chunks that each parse on their own:
// tags open at a cut are closed at the chunk's end and reopened at the start
// of the next one. Input must come from sanitizeHTML (balanced, whitelisted
// tags), which also makes a cut inside a tag token practically impossible —
// it would take thousands of runes without a newline.
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

// scanTags advances the open-tag stack over one chunk of sanitized HTML.
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
