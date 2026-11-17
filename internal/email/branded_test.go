package email

import (
	"context"
	"encoding/json"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/apohor/rivolt/internal/emailshell"
)

// captured posts a message through a Client and returns the JSON body Resend would have received.
func captured(t *testing.T, m Message) map[string]any {
	t.Helper()
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	c := New(Config{APIKey: "k", From: "Rivolt <hello@rivolt.dev>"})
	c.base = srv.URL
	m.To = "a@example.com"
	if err := c.Send(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	return got
}

// Every message that goes through Send carries the Rivolt shell, whichever sender built it and whatever HTML (or none) it brought.
func TestSendAlwaysBrandsTheHTMLPart(t *testing.T) {
	text := "Hi,\n\nYour Pro plan renews soon. Manage it: https://rivolt.dev/settings\n\nRivolt (support@rivolt.dev)\nUnsubscribe: https://rivolt.dev/u/x"
	cases := map[string]Message{
		"no html":       {Subject: "S", Text: text},
		"foreign html":  {Subject: "S", Text: text, HTML: "<p>Hi</p>"},
		"shell html":    {Subject: "S", Text: text, HTML: emailshell.FromText("S", text)},
		"with headers":  {Subject: "S", Text: text, Headers: map[string]string{"List-Unsubscribe": "<https://rivolt.dev/u/x>"}},
		"empty subject": {Text: text},
	}
	for name, m := range cases {
		got := captured(t, m)
		h, _ := got["html"].(string)
		if !emailshell.Branded(h) {
			t.Errorf("%s: html part is not in the Rivolt shell: %q", name, h)
		}
		if got["text"] != text {
			t.Errorf("%s: plain-text part changed: %q", name, got["text"])
		}
		if strings.Contains(h, "<img") || strings.Contains(h, "http://") || strings.Contains(h, "<script") {
			t.Errorf("%s: html has an image, insecure link or script", name)
		}
		for _, w := range []string{"https://rivolt.dev/settings", "https://rivolt.dev/u/x"} {
			if name != "foreign html" && !strings.Contains(h, w) {
				t.Errorf("%s: html lost %s", name, w)
			}
		}
	}
}

// Mail leaves Rivolt only through Send. A second path to Resend, or to an SMTP server, would skip the shell, so this walks every Go
// file and fails on one.
func TestNoOtherWayToSendMail(t *testing.T) {
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	err := filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.IsDir() {
			if n := fi.Name(); n == "node_modules" || n == ".git" || n == "vendor" || n == "email" && filepath.Base(filepath.Dir(p)) == "internal" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if strings.Contains(string(src), "api.resend.com") {
			t.Errorf("%s talks to Resend directly; use email.Client.Send so the message is branded", p)
		}
		f, err := parser.ParseFile(fset, p, src, parser.ImportsOnly)
		if err != nil {
			return nil
		}
		for _, im := range f.Imports {
			if im.Path.Value == `"net/smtp"` {
				t.Errorf("%s imports net/smtp; use email.Client.Send so the message is branded", p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
