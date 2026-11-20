package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
)

// ui writes what a person reads, in the style of GitHub's CLI: ✓ for a step that finished, ! for a warning,
// ✗ for a failure, - for anything else, and a spinner while waiting. Colour only marks the symbol, and
// colorprofile drops it when the output isn't a terminal, NO_COLOR is set or TERM is dumb.
type ui struct {
	mu   sync.Mutex
	w    io.Writer
	live bool // a person watches a terminal: the spinner runs
	now  func() time.Time

	status    func() string // the waiting line; nil when nothing is waited for
	frame     int
	drawn     bool // the spinner line is on screen
	stopTick  chan struct{}
	lastQuiet time.Time // without a terminal: when the waiting line was last printed
}

var (
	green  = lipgloss.NewStyle().Foreground(lipgloss.Green)
	red    = lipgloss.NewStyle().Foreground(lipgloss.Red)
	yellow = lipgloss.NewStyle().Foreground(lipgloss.Yellow)
	cyan   = lipgloss.NewStyle().Foreground(lipgloss.Cyan)
	faint  = lipgloss.NewStyle().Faint(true)
	bold   = lipgloss.NewStyle().Bold(true)
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func newUI(w io.Writer, live bool, now func() time.Time) *ui {
	return &ui{w: colorprofile.NewWriter(w, os.Environ()), live: live, now: now}
}

func (u *ui) ok(f string, a ...any)   { u.say(green.Render("✓"), f, a...) }
func (u *ui) warn(f string, a ...any) { u.say(yellow.Render("!"), f, a...) }
func (u *ui) fail(f string, a ...any) { u.say(red.Render("✗"), f, a...) }
func (u *ui) info(f string, a ...any) { u.say(faint.Render("-"), f, a...) }

// hint is an indented line under the one before it.
func (u *ui) hint(f string, a ...any) { u.say(" ", f, a...) }

// line is a plain line, for tables and blank lines.
func (u *ui) line(f string, a ...any) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.clear()
	fmt.Fprintf(u.w, f+"\n", a...)
	u.draw()
}

func (u *ui) say(sym, f string, a ...any) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.clear()
	fmt.Fprintf(u.w, "%s %s\n", sym, fmt.Sprintf(f, a...))
	u.draw()
}

// next prints the commands to run now, each with an optional note after it.
func (u *ui) next(cmds [][2]string) {
	width := 0
	for _, c := range cmds {
		width = max(width, len(c[0]))
	}
	u.line("")
	u.line("%s", bold.Render("Next:"))
	for _, c := range cmds {
		if c[1] == "" {
			u.line("  %s", c[0])
		} else {
			u.line("  %-*s    %s", width, c[0], faint.Render(c[1]))
		}
	}
}

// wait shows text as a spinner line rewritten in place, when a person watches a terminal. text is called
// each time the line is drawn. Without a terminal it does nothing; logs get still instead.
func (u *ui) wait(text func() string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.live {
		return
	}
	u.status = text
	if u.stopTick == nil {
		u.stopTick = make(chan struct{})
		go u.tick(u.stopTick)
	}
	u.draw()
}

// still notes a long wait in logs: the first call starts the clock, then text is printed once an hour.
func (u *ui) still(text string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	switch {
	case u.live:
	case u.lastQuiet.IsZero():
		u.lastQuiet = u.now()
	case u.now().Sub(u.lastQuiet) >= time.Hour:
		u.lastQuiet = u.now()
		fmt.Fprintf(u.w, "%s %s %s\n", faint.Render("-"), u.now().Format("15:04"), text)
	}
}

// done ends the wait and removes the spinner line.
func (u *ui) done() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.clear()
	u.status = nil
	if u.stopTick != nil {
		close(u.stopTick)
		u.stopTick = nil
	}
}

func (u *ui) tick(stop chan struct{}) {
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			u.mu.Lock()
			u.frame++
			u.draw()
			u.mu.Unlock()
		}
	}
}

func (u *ui) draw() {
	if !u.live || u.status == nil {
		return
	}
	fmt.Fprintf(u.w, "\r\033[K%s %s", cyan.Render(spinnerFrames[u.frame%len(spinnerFrames)]), u.status())
	u.drawn = true
}

func (u *ui) clear() {
	if u.drawn {
		fmt.Fprint(u.w, "\r\033[K")
		u.drawn = false
	}
}

// failure is an error written for a person: what went wrong, then what to do about it. code is the stop
// code agents see in the stopped event. shown means it was already printed.
type failure struct {
	code    string
	msg     string
	hints   []string
	shown   bool
	neutral bool  // printed with - rather than ✗, like a run you stopped yourself
	err     error // the cause, so exit codes still see Ctrl+C
}

func (f *failure) Unwrap() error { return f.err }

func (f *failure) Error() string {
	if len(f.hints) == 0 {
		return f.msg
	}
	h := strings.Join(f.hints, " ")
	return f.msg + "; " + strings.ToLower(h[:1]) + h[1:]
}

func fail(code, msg string, hints ...string) *failure {
	return &failure{code: code, msg: msg, hints: hints}
}

func (u *ui) report(f *failure) {
	if f.neutral {
		u.info("%s", f.msg)
	} else {
		u.fail("%s", f.msg)
	}
	for _, h := range f.hints {
		u.hint("%s", h)
	}
	f.shown = true
}

// since says how long ago t was, for the waiting lines: 40s, 3m10s, 2h05m.
func since(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}
