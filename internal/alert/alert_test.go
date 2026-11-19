package alert

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseKinds(t *testing.T) {
	links, err := Parse("https://ntfy.sh/closedvps-abc, https://discord.com/api/webhooks/1/x, https://hooks.slack.com/services/T/B/x," +
		" telegram://123:secret@telegram?chats=42,-100, ntfy://ntfy.example.com/mytopic, https://hooks.example.com/in")
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, l := range links {
		kinds = append(kinds, l.Kind)
	}
	if got := strings.Join(kinds, " "); got != "ntfy discord slack telegram ntfy webhook" {
		t.Fatalf("kinds: %s", got)
	}
	if tg := links[3]; tg.bot != "123:secret" || strings.Join(tg.chat, ",") != "42,-100" {
		t.Fatalf("telegram: %+v", tg)
	}
	if links[4].url != "https://ntfy.example.com/mytopic" {
		t.Fatalf("self-hosted ntfy: %s", links[4].url)
	}
	if strings.Contains(links[3].Name(), "secret") || strings.Contains(links[1].Name(), "/x") {
		t.Fatal("names must not show secrets")
	}
}

func TestParseRejects(t *testing.T) {
	for _, s := range []string{
		"http://example.com/hook", "example.com", "https://ntfy.sh/", "telegram://123@telegram",
		"https://example.com/a b", `https://example.com/"x`,
	} {
		if _, err := Parse(s); err == nil {
			t.Errorf("%q should be refused", s)
		}
	}
	if l, err := Parse("none"); err != nil || l != nil {
		t.Fatalf("none: %v %v", l, err)
	}
}

func TestSendReportsFailures(t *testing.T) {
	var got []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, r.Header.Get("Title")+"|"+string(b))
		if strings.HasSuffix(r.URL.Path, "/fail") {
			http.Error(w, "nope", http.StatusForbidden)
		}
	}))
	defer srv.Close()
	links := []Link{{Kind: "ntfy", url: srv.URL + "/topic"}, {Kind: "webhook", url: srv.URL + "/fail"}}
	errs := Send(context.Background(), srv.Client(), links, Message{Title: "vps bought", Text: "cx23 in nbg1"})
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "403") {
		t.Fatalf("errs: %v", errs)
	}
	if got[0] != "vps bought|cx23 in nbg1" {
		t.Fatalf("ntfy request: %q", got[0])
	}
}

func TestCurlConfigQuotes(t *testing.T) {
	r := Request{URL: "https://example.com/x", Header: map[string]string{"Title": `a "b"`}, Body: "line1\nline2 \\ end"}
	c := r.CurlConfig()
	for _, want := range []string{"globoff\n", `url = "https://example.com/x"`, `header = "Title: a \"b\""`, `data-binary = "line1\nline2 \\ end"`} {
		if !strings.Contains(c, want) {
			t.Fatalf("missing %s in:\n%s", want, c)
		}
	}
	if strings.Count(c, "\n") != 4 {
		t.Fatalf("each option must stay on one line:\n%s", c)
	}
}

// The file must come out the same every time: Redact finds it in the rendered setup by its exact text.
func TestCurlConfigSortsHeaders(t *testing.T) {
	r := Request{URL: "https://example.com", Header: map[string]string{"X-B": "2", "Content-Type": "json", "X-A": "1"}}
	first := r.CurlConfig()
	for range 20 {
		if r.CurlConfig() != first {
			t.Fatal("header order changes between calls")
		}
	}
	if !strings.Contains(first, "Content-Type: json\"\nheader = \"X-A: 1\"\nheader = \"X-B: 2") {
		t.Fatalf("headers not sorted:\n%s", first)
	}
}

// Only a bare chat id after a Telegram link belongs to it; anything else must be a link of its own.
func TestParseCommas(t *testing.T) {
	for _, s := range []string{"https://ntfy.sh/alerts, ntfy.sh/other", "https://ntfy.sh/alerts, 42", "telegram://1:x@telegram?chats=42, hooks.example.com"} {
		if l, err := Parse(s); err == nil {
			t.Errorf("%q gave %d link(s) instead of an error", s, len(l))
		}
	}
}

func TestErrorsHideSecrets(t *testing.T) {
	_, err := Parse("http://hooks.example.com/services/T000/B000/s3cretpath")
	if err == nil || strings.Contains(err.Error(), "s3cret") || !strings.Contains(err.Error(), "hooks.example.com") {
		t.Fatalf("error should name the host only: %v", err)
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{"https://ntfy.sh/x", "telegram://1:abc@telegram?chats=1,-2", "ntfy://h/t", "https://discord.com/api/webhooks/1/x, https://a.b/c", "none", ","} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		links, err := Parse(s)
		if err != nil {
			return
		}
		for _, l := range links {
			if l.bot != "" && strings.Contains(l.Name(), l.bot) {
				t.Fatalf("name %q shows the bot token", l.Name())
			}
			for _, r := range l.Requests(Message{Title: "t", Text: "x"}) {
				if !strings.HasPrefix(r.URL, "https://") {
					t.Fatalf("%q sends to %q", s, r.URL)
				}
			}
		}
	})
}

// unquote reverses CurlConfig's quoting of one value.
func unquote(t *testing.T, s string) string {
	t.Helper()
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		t.Fatalf("not quoted: %q", s)
	}
	var b strings.Builder
	for i := 1; i < len(s)-1; i++ {
		c := s[i]
		if c == '"' {
			t.Fatalf("bare quote in %q", s)
		}
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		i++
		b.WriteByte(map[byte]byte{'\\': '\\', '"': '"', 'n': '\n', 'r': '\r', 't': '\t'}[s[i]])
	}
	return b.String()
}

// Whatever goes in comes back out of curl's quoting unchanged, on the lines it belongs on.
func FuzzCurlConfig(f *testing.F) {
	f.Add("https://e.com/x", "Title", "a \"b\"\n", "line1\nline2 \\ end\r\t")
	f.Fuzz(func(t *testing.T, url, hk, hv, body string) {
		r := Request{URL: url, Header: map[string]string{hk: hv}, Body: body}
		lines := strings.Split(strings.TrimSuffix(r.CurlConfig(), "\n"), "\n")
		if len(lines) != 4 || lines[0] != "globoff" {
			t.Fatalf("lines: %q", lines)
		}
		for i, want := range []string{"url = ", "header = ", "data-binary = "} {
			if !strings.HasPrefix(lines[i+1], want) {
				t.Fatalf("line %d: %q", i+1, lines[i+1])
			}
		}
		if got := unquote(t, strings.TrimPrefix(lines[1], "url = ")); got != url {
			t.Fatalf("url %q came back as %q", url, got)
		}
		if got := unquote(t, strings.TrimPrefix(lines[2], "header = ")); got != hk+": "+hv {
			t.Fatalf("header came back as %q", got)
		}
		if got := unquote(t, strings.TrimPrefix(lines[3], "data-binary = ")); got != body {
			t.Fatalf("body %q came back as %q", body, got)
		}
	})
}
