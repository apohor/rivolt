package emailshell

import (
	"html"
	"regexp"
	"strings"
	"testing"
)

var tagRE = regexp.MustCompile(`(?s)<style>.*?</style>|<title>.*?</title>|<br>|<[^>]+>`)

// words is the visible text of an HTML part or a plain-text part, as the sequence of its words.
func words(s string) []string {
	s = strings.ReplaceAll(s, "<br>", " ")
	s = strings.ReplaceAll(s, "</p>", " ")
	s = strings.ReplaceAll(s, "</td>", " ")
	return strings.Fields(html.UnescapeString(tagRE.ReplaceAllString(s, "")))
}

// Strip the mark and wordmark the shell adds, leaving what the text said.
func body(h string) []string {
	w := words(h)
	if len(w) < 2 || w[0] != "ϟ" || w[1] != "Rivolt" {
		return w
	}
	return w[2:]
}

func TestFromTextSaysExactlyTheTextWords(t *testing.T) {
	text := "Hi,\n\nFrom November 16, 2026 there is a free tier & Pro <b>.\nSee https://rivolt.dev/terms. Or (https://rivolt.dev/privacy).\n\nThe Rivolt team\nRivolt is unofficial and not affiliated with Rivian.\n"
	got, want := strings.Join(body(FromText("Subject <x>", text)), " "), strings.Join(strings.Fields(text), " ")
	if got != want {
		t.Fatalf("html words\n%q\ntext words\n%q", got, want)
	}
}

func TestFromTextLinksAndEscapes(t *testing.T) {
	h := FromText("T", "Read https://rivolt.dev/terms/2026-10-23, then <script>alert(1)</script>.\n\nBye")
	if !strings.Contains(h, `<a href="https://rivolt.dev/terms/2026-10-23" `) || !strings.Contains(h, `>https://rivolt.dev/terms/2026-10-23</a>,`) {
		t.Errorf("link or its trailing comma wrong: %s", h)
	}
	if strings.Contains(h, "<script>") || !strings.Contains(h, "&lt;script&gt;") {
		t.Errorf("markup in the text was not escaped: %s", h)
	}
}

func TestShellHasTheMarkTheMetaAndTheCard(t *testing.T) {
	h := FromText("Subject & more", "One\n\nTwo")
	for _, want := range []string{`<meta name="color-scheme" content="light dark">`, `max-width:560px`, `#10b981`, `&#991;`, `prefers-color-scheme: dark`, `<title>Subject &amp; more</title>`} {
		if !strings.Contains(h, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(h, "<img") || strings.Contains(h, "src=") {
		t.Error("the shell must not load anything remote")
	}
}
