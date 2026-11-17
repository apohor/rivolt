// Package emailshell is the Rivolt look for HTML email: a 560 px card under an emerald bolt mark, light and dark, system fonts, no images
// and no remote content. FromText wraps a plain-text body in it word for word, so the HTML part can never say something the text part
// does not. Every email Rivolt sends goes out in it: internal/email wraps any message that arrives without the shell.
package emailshell

import (
	"html"
	"regexp"
	"strings"
)

const (
	emerald    = "#10b981"
	emeraldDim = "#047857"
)

// marker is the class on the card; Branded looks for it.
const marker = `class="rv-card"`

// Branded reports whether h is a document built by Wrap.
func Branded(h string) bool { return strings.Contains(h, marker) }

var urlRE = regexp.MustCompile(`https?://[^\s<>"]+`)

// Wrap puts bodyHTML (already escaped, trusted markup) in the shell. title is the subject: it becomes the document title.
func Wrap(title, bodyHTML string) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="color-scheme" content="light dark"><meta name="supported-color-schemes" content="light dark"><title>`)
	b.WriteString(html.EscapeString(title))
	b.WriteString(`</title><style>`)
	b.WriteString(`@media (prefers-color-scheme: dark){body,.rv-page{background:#0b0f0d !important;color:#e8ece9 !important}.rv-card{background:#141a17 !important}.rv-muted{color:#9aa6a0 !important}.rv-rule{border-top-color:#26302b !important}a{color:#34d399 !important}}`)
	b.WriteString(`</style></head><body class="rv-page" style="margin:0;padding:24px;background:#f6f7f8;color:#111;font:16px/1.5 -apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif">`)
	b.WriteString(`<div ` + marker + ` style="max-width:560px;margin:0 auto;background:#fff;border-radius:10px;padding:24px">`)
	// The mark is text on an emerald square, so no client has an image to block.
	b.WriteString(`<table role="presentation" style="border-collapse:collapse;margin:0 0 20px"><tr><td style="width:32px;height:32px;background:` + emerald + `;border-radius:8px;text-align:center;color:#fff;font-size:18px;line-height:32px;font-weight:700">&#991;</td><td style="padding-left:10px;font-size:20px;font-weight:700;letter-spacing:.2px">Rivolt</td></tr></table>`)
	b.WriteString(bodyHTML)
	b.WriteString(`</div></body></html>`)
	return b.String()
}

// FromText renders a plain-text email as HTML in the shell. Blank lines separate paragraphs, single newlines stay line breaks, and
// every http(s) address becomes a link whose text is the address itself. The last paragraph (the sign-off and the not-affiliated line)
// is set small and muted; the words are unchanged.
func FromText(title, text string) string { return FromTextCTA(title, text, "", "") }

// FromTextCTA is FromText with one primary action: an emerald button labelled label that links to href, placed above the paragraph
// that holds the plain link (the fallback for clients that block buttons). href must be http(s) and appear in text; otherwise there
// is no button. The text part is not touched.
func FromTextCTA(title, text, label, href string) string {
	cta := label != "" && urlRE.FindString(href) == href && strings.Contains(text, href)
	paras := strings.Split(strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n")), "\n\n")
	var b strings.Builder
	for i, p := range paras {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		style := `margin:0 0 16px`
		cls := ""
		if i == len(paras)-1 && len(paras) > 1 {
			style = `margin:20px 0 0;padding-top:16px;border-top:1px solid #ddd;color:#666;font-size:13px`
			cls = ` class="rv-muted rv-rule"`
		}
		if cta && strings.Contains(p, href) {
			b.WriteString(`<p style="margin:0 0 16px"><a href="` + html.EscapeString(href) + `" style="display:inline-block;padding:10px 18px;background:` + emerald + `;color:#fff;font-weight:600;border-radius:6px;text-decoration:none">` + html.EscapeString(label) + `</a></p>`)
			cta = false
		}
		b.WriteString(`<p` + cls + ` style="` + style + `">` + linkify(p) + `</p>`)
	}
	return Wrap(title, b.String())
}

// linkify escapes s and turns each URL into a link; trailing sentence punctuation stays outside the link.
func linkify(s string) string {
	var b strings.Builder
	last := 0
	for _, loc := range urlRE.FindAllStringIndex(s, -1) {
		start, end := loc[0], loc[1]
		for end > start && strings.ContainsRune(".,;:)!?", rune(s[end-1])) {
			end--
		}
		b.WriteString(escapeLines(s[last:start]))
		u := s[start:end]
		b.WriteString(`<a href="` + html.EscapeString(u) + `" style="color:` + emeraldDim + `">` + html.EscapeString(u) + `</a>`)
		last = end
	}
	b.WriteString(escapeLines(s[last:]))
	return b.String()
}

func escapeLines(s string) string {
	return strings.ReplaceAll(html.EscapeString(s), "\n", "<br>")
}
