// Package buy decides what may be bought and keeps trying until Hetzner sells it: the candidates under each
// price cap, the total cap across the project, and the retry loop.
package buy

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"

	"github.com/tulayha/closedvps/internal/cloud"
)

type Candidate struct {
	Type     string  `json:"type"`
	Arch     string  `json:"arch"`
	Cores    int     `json:"cores"`
	Memory   float64 `json:"memory_gb"`
	Location string  `json:"location"`
	Price    float64 `json:"price"` // per month before VAT, with IPv4 and backups when on
}

func (c Candidate) String() string { return c.Type + " " + c.Location }

// ValidPrice is a usable cap: above 0 and finite. NaN fails the comparison, so it's refused too.
func ValidPrice(v float64) bool { return v > 0 && !math.IsInf(v, 0) }

// ParseTypes reads a type list where each type may carry its own price cap: "cx23:6,cx33:12".
func ParseTypes(list []string) ([]string, map[string]float64, error) {
	var names []string
	caps := map[string]float64{}
	for _, t := range list {
		name, capText, hasCap := strings.Cut(t, ":")
		if hasCap {
			v, err := strconv.ParseFloat(strings.ReplaceAll(capText, ",", "."), 64)
			if err != nil || !ValidPrice(v) {
				return nil, nil, fmt.Errorf("%q: a type's price cap is a number above 0, like cx23:6", t)
			}
			caps[name] = v
		}
		names = append(names, name)
	}
	return names, caps, nil
}

// Wants is what the user will buy.
type Wants struct {
	Types     []string           // in the order to try them; empty: every shared-vCPU type, cheapest first
	Caps      map[string]float64 // a type's own cap, over MaxPrice
	Locations []string           // in the order to try them
	MaxPrice  float64            // per server; 0 is no limit (only `types` uses that)
	Backups   bool
}

// Candidates lists what may be bought, in the order it's tried: the wanted types in their order, or with
// none given every shared-vCPU type cheapest first; then locations in their order. Anything over its cap,
// with an unknown price, or being retired in a location is left out.
func (w Wants) Candidates(types []*hcloud.ServerType, ipv4 map[string]float64) []Candidate {
	var out []Candidate
	for _, t := range types {
		if len(w.Types) > 0 && !slices.Contains(w.Types, t.Name) {
			continue
		}
		if len(w.Types) == 0 && t.CPUType != hcloud.CPUTypeShared {
			continue
		}
		limit := w.MaxPrice
		if c, ok := w.Caps[t.Name]; ok {
			limit = c
		}
		for _, l := range t.Locations {
			if l.Location == nil || !slices.Contains(w.Locations, l.Location.Name) || l.Deprecation != nil {
				continue
			}
			p, ok := Monthly(t, l.Location.Name, ipv4, w.Backups)
			if !ok || (limit > 0 && p > limit+1e-9) {
				continue
			}
			out = append(out, candidate(t, l.Location.Name, p))
		}
	}
	slices.SortStableFunc(out, func(a, b Candidate) int {
		if len(w.Types) > 0 {
			if c := cmp.Compare(slices.Index(w.Types, a.Type), slices.Index(w.Types, b.Type)); c != 0 {
				return c
			}
		} else if c := cmp.Compare(a.Price, b.Price); c != 0 {
			return c
		}
		return cmp.Compare(slices.Index(w.Locations, a.Location), slices.Index(w.Locations, b.Location))
	})
	return out
}

func candidate(t *hcloud.ServerType, location string, price float64) Candidate {
	return Candidate{t.Name, string(t.Architecture), t.Cores, float64(t.Memory), location, price}
}

// Monthly is what a type costs per month in a location, before VAT: the server, backups when on, and IPv4.
func Monthly(t *hcloud.ServerType, location string, ipv4 map[string]float64, backups bool) (float64, bool) {
	p, ok := cloud.ServerPrice(t, location)
	ip, ipOK := ipv4[location]
	if !ok || !ipOK {
		return 0, false
	}
	if backups {
		p *= 1 + cloud.BackupShare
	}
	return p + ip, true
}

// Availability maps "type location" to Hetzner's in-stock hint.
func Availability(types []*hcloud.ServerType) map[string]bool {
	m := map[string]bool{}
	for _, t := range types {
		for _, l := range t.Locations {
			if l.Location != nil {
				m[t.Name+" "+l.Location.Name] = l.Available
			}
		}
	}
	return m
}

// stopCodes can't be fixed by waiting. Everything else, including whatever code Hetzner uses for its block
// on new accounts, is retried.
var stopCodes = []string{"unauthorized", "invalid_input", "json_error", "not_found", "resource_limit_exceeded", "protected", "uniqueness_error"}

var ErrOverTotal = errors.New("over the total cap")

func IsFatal(err error) bool {
	if errors.Is(err, ErrOverTotal) || cloud.IsCode(err, stopCodes...) {
		return true
	}
	// A read-only token gets "forbidden: insufficient permissions"; other forbidden replies may be the
	// stock block, which waiting can fix.
	return cloud.IsCode(err, "forbidden") && strings.Contains(strings.ToLower(err.Error()), "permission")
}

// API is the part of Hetzner the buyer uses; *cloud.Client has it.
type API interface {
	ServerTypes(ctx context.Context) ([]*hcloud.ServerType, error)
	Prices(ctx context.Context) (cloud.Prices, error)
	Create(ctx context.Context, o hcloud.ServerCreateOpts) (hcloud.ServerCreateResult, error)
	ServerByName(ctx context.Context, name string) (*hcloud.Server, error)
	Managed(ctx context.Context) ([]*hcloud.Server, error)
}

type Purchase struct {
	Server    *hcloud.Server
	Action    *hcloud.Action // nil when the create's reply was lost and the server was found afterwards
	Candidate Candidate      // what it really is and costs
}

// Buyer fills Slots, one server each, with whatever Hetzner sells.
type Buyer struct {
	API      API
	Slots    []hcloud.ServerCreateOpts // ServerType and Location are set per try
	Wants    Wants
	MaxTotal float64 // most all closedvps servers in the project may cost per month; 0 is no limit
	Interval time.Duration
	Log      io.Writer
	Currency string
	Sleep    func(ctx context.Context, d time.Duration) error
	Now      func() time.Time
	Bought   func(p Purchase, n int) // as each server is bought, n counting from 1
	Waiting  func(check, bought int)

	got    []Purchase
	posted map[string]bool // names a create was sent for
	seen   map[string]bool // refusal messages already printed
}

// Run buys until every slot is filled, a refusal waiting can't fix, or ctx ends. It returns what was bought,
// also when it stops early.
func (b *Buyer) Run(ctx context.Context) ([]Purchase, error) {
	b.posted, b.seen = map[string]bool{}, map[string]bool{}
	for check := 1; ; check++ {
		err := b.check(ctx, check)
		if len(b.got) == len(b.Slots) {
			return b.got, nil
		}
		if err != nil {
			return b.got, err
		}
		if check == 1 {
			fmt.Fprintf(b.Log, "Trying again every %s until %s bought. Ctrl+C stops.\n", Every(b.Interval), plural(len(b.Slots)-len(b.got), "one is", "they're all"))
		}
		if b.Waiting != nil {
			b.Waiting(check, len(b.got))
		}
		if err := b.Sleep(ctx, b.Interval); err != nil {
			return b.got, err
		}
	}
}

// Recover looks, after Run stopped early, for servers Hetzner made although their create got no reply.
// It returns them, and the names it couldn't check, which may be billing.
func (b *Buyer) Recover(ctx context.Context) (found []Purchase, unknown []string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	prices, _ := b.API.Prices(ctx) // without prices a found server still counts, at 0
	for _, o := range b.Slots {
		if !b.posted[o.Name] || b.has(o.Name) {
			continue
		}
		s, err := b.API.ServerByName(ctx, o.Name)
		switch {
		case err != nil:
			unknown = append(unknown, o.Name)
		case cloud.IsManaged(s):
			p := Purchase{Server: s, Candidate: b.real(s, prices.IPv4)}
			b.got = append(b.got, p)
			found = append(found, p)
		}
	}
	return found, unknown
}

func (b *Buyer) has(name string) bool {
	return slices.ContainsFunc(b.got, func(p Purchase) bool { return p.Server.Name == name })
}

// real is what a server this run made is and costs, from Hetzner's record of it: a create whose reply was
// lost may have been for another candidate than the one being tried now.
func (b *Buyer) real(s *hcloud.Server, ipv4 map[string]float64) Candidate {
	loc := ""
	if s.Location != nil {
		loc = s.Location.Name
	}
	p, _ := Monthly(s.ServerType, loc, ipv4, b.Wants.Backups)
	if s.ServerType == nil {
		return Candidate{Location: loc, Price: p}
	}
	return candidate(s.ServerType, loc, p)
}

// Spending is what running servers cost per month, before VAT: backups only where they're on.
func Spending(servers []*hcloud.Server, ipv4 map[string]float64) float64 {
	var sum float64
	for _, s := range servers {
		loc := ""
		if s.Location != nil {
			loc = s.Location.Name
		}
		p, _ := cloud.ServerPrice(s.ServerType, loc)
		if s.BackupWindow != "" {
			p *= 1 + cloud.BackupShare
		}
		if cloud.IPv4(s) != "" {
			p += ipv4[loc]
		}
		sum += p
	}
	return sum
}

// spending is Spending, except that this run's servers count with backups when they're wanted, since
// backups are turned on only once buying is done.
func (b *Buyer) spending(servers []*hcloud.Server, ipv4 map[string]float64) float64 {
	mine := map[string]bool{}
	for _, o := range b.Slots {
		mine[o.Name] = true
	}
	var others []*hcloud.Server
	var sum float64
	for _, s := range servers {
		if mine[s.Name] {
			sum += b.real(s, ipv4).Price
		} else {
			others = append(others, s)
		}
	}
	return sum + Spending(others, ipv4)
}

// check is one look at the stock, buying what it can. Only errors waiting can't fix are returned.
func (b *Buyer) check(ctx context.Context, check int) error {
	retry := func(what string, err error) error {
		if IsFatal(err) || ctx.Err() != nil {
			return err
		}
		fmt.Fprintf(b.Log, "%s  %s failed: %v\n", b.clock(), what, err)
		return nil
	}
	types, err := b.API.ServerTypes(ctx)
	if err != nil {
		return retry("checking stock", err)
	}
	prices, err := b.API.Prices(ctx)
	if err != nil {
		return retry("reading prices", err)
	}
	cands := b.Wants.Candidates(types, prices.IPv4)
	if len(cands) == 0 {
		return errors.New("nothing on your list is under its price cap any more; see `closedvps types`")
	}
	var spent float64
	if b.MaxTotal > 0 {
		servers, err := b.API.Managed(ctx)
		if err != nil {
			return retry("reading your servers", err)
		}
		spent = b.spending(servers, prices.IPv4)
		cheapest := slices.MinFunc(cands, func(a, c Candidate) int { return cmp.Compare(a.Price, c.Price) })
		if spent+cheapest.Price > b.MaxTotal+1e-9 {
			return fmt.Errorf("%w: your closedvps servers already cost %s and the cheapest on your list adds %s, over --max-total %s",
				ErrOverTotal, Money(spent, b.Currency), Money(cheapest.Price, b.Currency), Money(b.MaxTotal, b.Currency))
		}
	}
	avail := Availability(types)
	before, tried := len(b.got), 0
	for len(b.got) < len(b.Slots) {
		slot := b.Slots[len(b.got)]
		var bought bool
		for _, c := range cands {
			if !avail[c.String()] || (b.MaxTotal > 0 && spent+c.Price > b.MaxTotal+1e-9) {
				continue
			}
			tried++
			p, err := b.create(ctx, slot, c, types, prices.IPv4)
			if err == nil {
				b.got = append(b.got, p)
				spent += p.Candidate.Price
				fmt.Fprintf(b.Log, "%s  %-12s %s bought, %s\n", b.clock(), p.Candidate, slot.Name, Money(p.Candidate.Price, b.Currency))
				if b.Bought != nil {
					b.Bought(p, len(b.got))
				}
				bought = true
				break
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if IsFatal(err) {
				return fmt.Errorf("Hetzner refused %s: %w", c, err)
			}
			if msg := err.Error(); !b.seen[msg] {
				b.seen[msg] = true
				fmt.Fprintf(b.Log, "%s  %-12s refused: %s\n", b.clock(), c, msg)
			} else {
				fmt.Fprintf(b.Log, "%s  %-12s refused again\n", b.clock(), c)
			}
		}
		if !bought {
			break
		}
	}
	switch left := len(b.Slots) - len(b.got); {
	case left == 0:
	case tried == 0 && before == 0:
		fmt.Fprintf(b.Log, "%s  nothing on your list is in stock (check %d)\n", b.clock(), check)
	case len(b.got) > 0 && (len(b.got) > before || tried == 0):
		fmt.Fprintf(b.Log, "%s  %d of %d bought, waiting for %d more (check %d)\n", b.clock(), len(b.got), len(b.Slots), left, check)
	}
	return nil
}

// create tries one candidate for one slot. When the reply says the name is taken, or no reply came, Hetzner
// may have made the server anyway, so it looks the name up before calling it a failure.
func (b *Buyer) create(ctx context.Context, slot hcloud.ServerCreateOpts, c Candidate, types []*hcloud.ServerType, ipv4 map[string]float64) (Purchase, error) {
	o := slot
	for _, t := range types {
		if t.Name == c.Type {
			o.ServerType = t
		}
	}
	o.Location = &hcloud.Location{Name: c.Location}
	b.posted[o.Name] = true
	res, err := b.API.Create(ctx, o)
	if err == nil {
		return Purchase{Server: res.Server, Action: res.Action, Candidate: c}, nil
	}
	if ctx.Err() == nil && (cloud.IsCode(err, "uniqueness_error") || !cloud.IsAPIError(err)) {
		if s, lookErr := b.API.ServerByName(ctx, o.Name); lookErr == nil && cloud.IsManaged(s) {
			return Purchase{Server: s, Candidate: b.real(s, ipv4)}, nil
		}
	}
	return Purchase{}, err
}

func (b *Buyer) clock() string {
	if b.Now == nil {
		return time.Now().Format("15:04:05")
	}
	return b.Now().Format("15:04:05")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// Every says an interval the way people do: "minute", "5 minutes", "30 seconds".
func Every(d time.Duration) string {
	switch {
	case d == time.Minute:
		return "minute"
	case d%time.Minute == 0:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	default:
		return fmt.Sprintf("%d seconds", int(d.Seconds()))
	}
}

func Money(v float64, currency string) string {
	if currency == "" {
		currency = "EUR"
	}
	return fmt.Sprintf("%.2f %s/month", v, currency)
}
