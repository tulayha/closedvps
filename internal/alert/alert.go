// Package alert turns the links people paste (ntfy, Discord, Slack, Telegram, any webhook) into the HTTP
// requests that deliver a message there. The same requests are sent from this computer and, for the "ready"
// alert, from the new server itself.
package alert

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

type Link struct {
	Kind string // ntfy, discord, slack, telegram, webhook
	Raw  string
	url  string   // where requests go (ntfy topic, webhook)
	bot  string   // telegram bot token
	chat []string // telegram chat ids
}

// Request is one HTTP POST that delivers a message.
type Request struct {
	URL    string
	Header map[string]string
	Body   string
}

// Message is what every link gets: a title, one line of text, and for webhooks the event as data.
type Message struct {
	Title string
	Text  string
	Data  any
}

// Parse reads a comma-separated list of links. Empty or "none" means no links.
func Parse(s string) ([]Link, error) {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "none") {
		return nil, nil
	}
	// Links are comma-separated, but a Telegram link lists its chat ids with commas too, so a bare chat id
	// after a Telegram link belongs to it. Anything else is a link of its own, and fails if it isn't one.
	var raws []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if n := len(raws); n > 0 && strings.HasPrefix(raws[n-1], "telegram://") && chatID.MatchString(p) {
			raws[n-1] += "," + p
			continue
		}
		raws = append(raws, p)
	}
	var out []Link
	for _, raw := range raws {
		l, err := parseOne(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, nil
}

func parseOne(raw string) (Link, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return Link{}, fmt.Errorf("%q isn't a link; paste the whole https://... address", short(raw))
	}
	if strings.ContainsAny(raw, " \t\r\n\"'\\") {
		return Link{}, fmt.Errorf("%q has spaces or quotes; paste only the link", short(raw))
	}
	switch u.Scheme {
	case "telegram":
		// telegram://<bot token>@telegram?chats=<id>[,<id>], the format other notification tools use. A bot token
		// is "<number>:<secret>", which a URL reads as user and password.
		pass, _ := u.User.Password()
		chats := strings.Split(u.Query().Get("chats"), ",")
		if u.User.Username() == "" || pass == "" || slices.ContainsFunc(chats, func(c string) bool { return !telegramChat.MatchString(c) }) {
			return Link{}, errors.New("a Telegram link looks like telegram://<bot token>@telegram?chats=<chat id>; `closedvps setup` builds it for you")
		}
		return Link{Kind: "telegram", Raw: raw, bot: u.User.Username() + ":" + pass, chat: chats}, nil
	case "ntfy":
		// ntfy://<host>/<topic> for a self-hosted ntfy server.
		if strings.Trim(u.Path, "/") == "" {
			return Link{}, errors.New("an ntfy link needs a topic: ntfy://<server>/<topic>")
		}
		return Link{Kind: "ntfy", Raw: raw, url: "https://" + u.Host + u.Path}, nil
	case "https":
	default:
		return Link{}, fmt.Errorf("%q: alert links start with https://", short(raw))
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case host == "ntfy.sh":
		if strings.Trim(u.Path, "/") == "" {
			return Link{}, errors.New("an ntfy link needs a topic: https://ntfy.sh/<topic>")
		}
		return Link{Kind: "ntfy", Raw: raw, url: u.String()}, nil
	case (host == "discord.com" || host == "discordapp.com") && strings.HasPrefix(u.Path, "/api/webhooks/"):
		return Link{Kind: "discord", Raw: raw, url: u.String()}, nil
	case host == "hooks.slack.com":
		return Link{Kind: "slack", Raw: raw, url: u.String()}, nil
	}
	return Link{Kind: "webhook", Raw: raw, url: u.String()}, nil
}

var (
	chatID = regexp.MustCompile(`^-?[0-9]+$`)
	// telegramChat is what Telegram takes as a chat_id: a number, or a public channel's @name.
	telegramChat = regexp.MustCompile(`^(-?[0-9]+|@[A-Za-z0-9_]{5,32})$`)
)

// short names a link in an error without its secret part (a webhook path or token): only scheme and host.
func short(s string) string {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		if len(s) > 12 {
			return s[:12] + "..."
		}
		return s
	}
	return u.Scheme + "://" + u.Hostname() + "/..."
}

// Name says where a link goes without showing its secret part.
func (l Link) Name() string {
	switch l.Kind {
	case "ntfy":
		u, _ := url.Parse(l.url)
		return "ntfy topic " + strings.Trim(u.Path, "/")
	case "discord":
		return "Discord webhook"
	case "slack":
		return "Slack webhook"
	case "telegram":
		return "Telegram chat " + strings.Join(l.chat, ", ")
	}
	u, _ := url.Parse(l.url)
	return "webhook at " + u.Host
}

// Requests are the POSTs that deliver m to this link.
func (l Link) Requests(m Message) []Request {
	jsonBody := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	jsonHeader := map[string]string{"Content-Type": "application/json"}
	switch l.Kind {
	case "ntfy":
		return []Request{{URL: l.url, Header: map[string]string{"Title": m.Title}, Body: m.Text}}
	case "discord":
		return []Request{{URL: l.url, Header: jsonHeader, Body: jsonBody(map[string]string{"content": "**" + m.Title + "**\n" + m.Text})}}
	case "slack":
		return []Request{{URL: l.url, Header: jsonHeader, Body: jsonBody(map[string]string{"text": "*" + m.Title + "*\n" + m.Text})}}
	case "telegram":
		var out []Request
		for _, c := range l.chat {
			out = append(out, Request{
				URL: "https://api.telegram.org/bot" + l.bot + "/sendMessage", Header: jsonHeader,
				Body: jsonBody(map[string]string{"chat_id": c, "text": m.Title + "\n" + m.Text}),
			})
		}
		return out
	}
	return []Request{{URL: l.url, Header: jsonHeader, Body: jsonBody(map[string]any{"title": m.Title, "text": m.Text, "event": m.Data})}}
}

// Send delivers m to every link and returns the failures, one per link.
func Send(ctx context.Context, c *http.Client, links []Link, m Message) []error {
	var errs []error
	for _, l := range links {
		for _, r := range l.Requests(m) {
			if err := post(ctx, c, r); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", l.Name(), err))
				break
			}
		}
	}
	return errs
}

func post(ctx context.Context, c *http.Client, r Request) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.URL, strings.NewReader(r.Body))
	if err != nil {
		return err
	}
	for k, v := range r.Header {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		// The error text repeats the URL, which can hold a token.
		var ue *url.Error
		if errors.As(err, &ue) {
			return ue.Err
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

// CurlConfig writes r as a curl config file (curl -K), so a shell never sees the link or the message.
func (r Request) CurlConfig() string {
	q := func(s string) string {
		s = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`).Replace(s)
		return `"` + s + `"`
	}
	var b strings.Builder
	// globoff: curl would otherwise read [] and {} in the URL as ranges and send several requests.
	b.WriteString("globoff\n")
	fmt.Fprintf(&b, "url = %s\n", q(r.URL))
	keys := make([]string, 0, len(r.Header))
	for k := range r.Header {
		keys = append(keys, k)
	}
	slices.Sort(keys) // the same request always gives the same file, which Redact relies on
	for _, k := range keys {
		fmt.Fprintf(&b, "header = %s\n", q(k+": "+r.Header[k]))
	}
	fmt.Fprintf(&b, "data-binary = %s\n", q(r.Body))
	return b.String()
}

// TelegramLink is the link for a bot token and a chat.
func TelegramLink(token, chat string) string {
	return "telegram://" + token + "@telegram?chats=" + chat
}

// TelegramChat finds the chat of the latest message sent to the bot, so nobody has to look up a chat id.
func TelegramChat(ctx context.Context, c *http.Client, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.telegram.org/bot"+token+"/getUpdates", nil)
	if err != nil {
		return "", errors.New("that doesn't look like a bot token")
	}
	resp, err := c.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			return "", ue.Err
		}
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		OK     bool `json:"ok"`
		Result []struct {
			Message *struct {
				Chat struct {
					ID int64 `json:"id"`
				} `json:"chat"`
			} `json:"message"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || !out.OK {
		return "", errors.New("Telegram didn't accept that bot token")
	}
	for i := len(out.Result) - 1; i >= 0; i-- {
		if m := out.Result[i].Message; m != nil {
			return fmt.Sprint(m.Chat.ID), nil
		}
	}
	return "", errors.New("no message to the bot yet; send it one in Telegram and try again")
}
