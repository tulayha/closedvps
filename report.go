package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/tulayha/closedvps/internal/alert"
	"github.com/tulayha/closedvps/internal/buy"
)

// eventKinds are the values of event.Event, in the order a run can produce them.
var eventKinds = []string{"plan", "waiting", "bought", "ready", "unreachable", "stopped"}

// event is one step of a create run, printed as a JSON line with --json. Bought and Count are pointers so a
// 0 still shows where it means something.
type event struct {
	V          int             `json:"v"`
	Event      string          `json:"event"` // one of eventKinds
	Time       time.Time       `json:"time"`
	Server     string          `json:"server,omitempty"`
	Type       string          `json:"type,omitempty"`
	Location   string          `json:"location,omitempty"`
	Price      float64         `json:"price,omitempty"`
	Currency   string          `json:"currency,omitempty"`
	IPv4       string          `json:"ipv4,omitempty"`
	SSH        string          `json:"ssh,omitempty"`
	Check      int             `json:"check,omitempty"`
	Bought     *int            `json:"bought,omitempty"`
	Count      *int            `json:"count,omitempty"`
	Reason     string          `json:"reason,omitempty"`
	Candidates []buy.Candidate `json:"candidates,omitempty"`
}

func num(n int) *int { return &n }

type reporter struct {
	json    *json.Encoder // nil without --json
	links   []alert.Link
	http    *http.Client
	log     io.Writer // where a failed alert is reported
	bell    io.Writer
	desktop func(title, text string)
	now     func() time.Time
}

func (r *reporter) emit(e event) {
	if r.json == nil {
		return
	}
	e.V, e.Time = 1, r.now().UTC().Truncate(time.Second)
	_ = r.json.Encode(e)
}

// alert tells the user something happened while they weren't watching: a bell and a desktop notification on
// this computer, and with remote set, the alert links too.
func (r *reporter) alert(title, text string, e event, remote bool) {
	_, _ = io.WriteString(r.bell, "\a")
	r.desktop(title, text)
	if !remote || len(r.links) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, err := range alert.Send(ctx, r.http, r.links, alert.Message{Title: title, Text: text, Data: e}) {
		fmt.Fprintf(r.log, "Couldn't send the alert to %v\n", err)
	}
}
