package bot

import (
	"regexp"
	"strings"
)

// LLM output is GitHub-flavoured markdown (## headings, **bold**, tables,
// fenced code). Telegram's legacy Markdown parser understands none of that and
// rejects the whole message, which used to lose answers outright and now shows
// raw markup via the plain-text fallback. Rendering to Telegram HTML instead is
// both safe (everything escapes cleanly) and closer to what the model meant.

var (
	reFence      = regexp.MustCompile("(?s)```[a-zA-Z0-9_+-]*\n(.*?)```")
	reInlineCode = regexp.MustCompile("`([^`\n]+)`")
	reHeading    = regexp.MustCompile(`(?m)^\s{0,3}#{1,6}\s+(.+)$`)
	reBoldStar   = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	reBoldUnd    = regexp.MustCompile(`__([^_\n]+)__`)
	reItalicStar = regexp.MustCompile(`(^|[\s(])\*([^*\n]+)\*($|[\s).,!?:;])`)
	reLink       = regexp.MustCompile(`\[([^\]\n]+)\]\(([^)\s]+)\)`)
	reBullet     = regexp.MustCompile(`(?m)^(\s*)[-*+]\s+`)
	reHR         = regexp.MustCompile(`(?m)^\s*(?:-{3,}|\*{3,}|_{3,})\s*$`)
)

const (
	codeToken   = "\x00CODE%d\x00"
	tokenFinder = "\x00CODE"
)

// renderTelegramHTML converts markdown-ish LLM output into Telegram HTML.
func renderTelegramHTML(src string) string {
	var blocks []string
	// Pull code out first so its contents are never treated as markup.
	src = reFence.ReplaceAllStringFunc(src, func(m string) string {
		body := reFence.FindStringSubmatch(m)[1]
		blocks = append(blocks, "<pre>"+escapeHTML(body)+"</pre>")
		return placeholder(len(blocks) - 1)
	})
	src = reInlineCode.ReplaceAllStringFunc(src, func(m string) string {
		body := reInlineCode.FindStringSubmatch(m)[1]
		blocks = append(blocks, "<code>"+escapeHTML(body)+"</code>")
		return placeholder(len(blocks) - 1)
	})

	out := escapeHTML(src)
	out = reHR.ReplaceAllString(out, "──────────")
	out = reHeading.ReplaceAllString(out, "<b>$1</b>")
	out = reLink.ReplaceAllString(out, `<a href="$2">$1</a>`)
	out = reBoldStar.ReplaceAllString(out, "<b>$1</b>")
	out = reBoldUnd.ReplaceAllString(out, "<b>$1</b>")
	out = reItalicStar.ReplaceAllString(out, "$1<i>$2</i>$3")
	out = reBullet.ReplaceAllString(out, "$1• ")

	for i, b := range blocks {
		out = strings.ReplaceAll(out, placeholder(i), b)
	}
	return out
}

func placeholder(i int) string {
	return "\x00CODE" + itoa(i) + "\x00"
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func escapeHTML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

// RenderTelegramHTMLForCheck exposes the renderer for tooling/tests.
func RenderTelegramHTMLForCheck(s string) string { return renderTelegramHTML(s) }
