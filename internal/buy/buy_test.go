package buy

import (
	"context"
	"errors"
	"math"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"

	"github.com/tulayha/closedvps/internal/cloud"
)

func st(name string, cpu hcloud.CPUType, arch string, prices map[string]string, avail map[string]bool) *hcloud.ServerType {
	t := &hcloud.ServerType{Name: name, CPUType: cpu, Architecture: hcloud.Architecture(arch), Cores: 2, Memory: 4}
	for loc, p := range prices {
		t.Pricings = append(t.Pricings, hcloud.ServerTypeLocationPricing{Location: &hcloud.Location{Name: loc}, Monthly: hcloud.Price{Net: p}})
		t.Locations = append(t.Locations, hcloud.ServerTypeLocation{Location: &hcloud.Location{Name: loc}, Available: avail[loc]})
	}
	return t
}

var testTypes = []*hcloud.ServerType{
	st("cx33", hcloud.CPUTypeShared, "x86", map[string]string{"nbg1": "6.00", "hel1": "6.00"}, map[string]bool{"hel1": true}),
	st("cx23", hcloud.CPUTypeShared, "x86", map[string]string{"nbg1": "4.00", "hel1": "4.00"}, map[string]bool{}),
	st("ccx13", hcloud.CPUTypeDedicated, "x86", map[string]string{"nbg1": "14.00"}, map[string]bool{"nbg1": true}),
	st("cax11", hcloud.CPUTypeShared, "arm", map[string]string{"fsn1": "4.50"}, map[string]bool{"fsn1": true}),
}

var ipv4 = map[string]float64{"nbg1": 0.5, "hel1": 0.5, "fsn1": 0.5}

func names(cs []Candidate) string {
	var s []string
	for _, c := range cs {
		s = append(s, c.String())
	}
	return strings.Join(s, ", ")
}

func TestCandidatesDefaultCheapestFirst(t *testing.T) {
	got := names(Wants{Locations: []string{"hel1", "nbg1", "fsn1"}, MaxPrice: 10}.Candidates(testTypes, ipv4))
	if want := "cx23 hel1, cx23 nbg1, cax11 fsn1, cx33 hel1, cx33 nbg1"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCandidatesGivenOrderAndCap(t *testing.T) {
	w := Wants{Types: []string{"cx33", "ccx13", "cx23"}, Locations: []string{"nbg1", "hel1"}, MaxPrice: 6.5}
	// ccx13 is over the cap; IPv4 counts towards it.
	if got, want := names(w.Candidates(testTypes, ipv4)), "cx33 nbg1, cx33 hel1, cx23 nbg1, cx23 hel1"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if c := (Wants{Locations: []string{"nbg1"}, MaxPrice: 4.4}).Candidates(testTypes, ipv4); len(c) != 0 {
		t.Fatalf("cap below server+IPv4 should leave nothing, got %s", names(c))
	}
}

func TestCandidatesMinMemory(t *testing.T) {
	small := st("cpx02", hcloud.CPUTypeShared, "x86", map[string]string{"nbg1": "3.00"}, map[string]bool{"nbg1": true})
	small.Memory = 1
	w := Wants{Locations: []string{"nbg1"}, MaxPrice: 10, MinMemory: 4}
	if got := names(w.Candidates(append([]*hcloud.ServerType{small}, testTypes...), ipv4)); got != "cx23 nbg1, cx33 nbg1" {
		t.Fatalf("a 1 GB type is too small for Claude Code or Codex: got %s", got)
	}
}

func TestCandidatesTypeCaps(t *testing.T) {
	w := Wants{Types: []string{"cx33", "cx23"}, Caps: map[string]float64{"cx33": 6}, Locations: []string{"nbg1"}, MaxPrice: 10}
	if got := names(w.Candidates(testTypes, ipv4)); got != "cx23 nbg1" {
		t.Fatalf("cx33 costs 6.50 with IPv4, over its own cap of 6: got %s", got)
	}
}

func TestCandidatesSkipUnknownIPv4Price(t *testing.T) {
	w := Wants{Types: []string{"cx23"}, Locations: []string{"nbg1", "hel1"}, MaxPrice: 10}
	if c := w.Candidates(testTypes, map[string]float64{"nbg1": 0.5}); names(c) != "cx23 nbg1" {
		t.Fatalf("got %s", names(c))
	}
}

func TestCandidatesWithBackups(t *testing.T) {
	c := Wants{Types: []string{"cx23"}, Locations: []string{"nbg1"}, Backups: true}.Candidates(testTypes, ipv4)
	if len(c) != 1 || c[0].Price != 4*1.2+0.5 {
		t.Fatalf("got %+v", c)
	}
	if c := (Wants{Types: []string{"cx23"}, Locations: []string{"nbg1"}, MaxPrice: 5, Backups: true}).Candidates(testTypes, ipv4); len(c) != 0 {
		t.Fatal("backups must count towards the cap")
	}
}

func TestCandidatesSkipDeprecated(t *testing.T) {
	types := []*hcloud.ServerType{st("cx23", hcloud.CPUTypeShared, "x86", map[string]string{"nbg1": "4"}, nil)}
	types[0].Locations[0].Deprecation = &hcloud.DeprecationInfo{UnavailableAfter: time.Now().Add(time.Hour)}
	if c := (Wants{Locations: []string{"nbg1"}}).Candidates(types, ipv4); len(c) != 0 {
		t.Fatalf("deprecated type offered: %s", names(c))
	}
}

func TestParseTypes(t *testing.T) {
	n, caps, err := ParseTypes([]string{"cx23:6", "cx33", "cax11:7,5"})
	if err != nil || strings.Join(n, ",") != "cx23,cx33,cax11" || caps["cx23"] != 6 || caps["cax11"] != 7.5 || len(caps) != 2 {
		t.Fatalf("got %v %v %v", n, caps, err)
	}
	for _, bad := range []string{"cx23:", "cx23:0", "cx23:-1", "cx23:x", "cx23:nan", "cx23:NaN", "cx23:inf", "cx23:+Inf"} {
		if _, caps, err := ParseTypes([]string{bad}); err == nil {
			t.Errorf("%s accepted as cap %v", bad, caps)
		}
	}
}

func TestValidPrice(t *testing.T) {
	for v, want := range map[float64]bool{10: true, 0.01: true, 0: false, -1: false, math.NaN(): false, math.Inf(1): false} {
		if ValidPrice(v) != want {
			t.Errorf("ValidPrice(%v) = %v", v, !want)
		}
	}
}

func apiErr(code, msg string) error { return hcloud.Error{Code: hcloud.ErrorCode(code), Message: msg} }

func TestIsFatal(t *testing.T) {
	cases := map[error]bool{
		apiErr("unauthorized", "x"):                                  true,
		apiErr("resource_limit_exceeded", "x"):                       true,
		apiErr("uniqueness_error", "x"):                              true,
		apiErr("forbidden", "insufficient permissions"):              true,
		apiErr("forbidden", "server creation is restricted"):         false,
		apiErr("resource_unavailable", "x"):                          false,
		apiErr("placement_error", "x"):                               false,
		apiErr("something_new", "x"):                                 false,
		errors.New("connection reset"):                               false,
		ErrOverTotal:                                                 true,
		errors.Join(errors.New("wrapped"), apiErr("protected", "x")): true,
	}
	for e, want := range cases {
		if got := IsFatal(e); got != want {
			t.Errorf("IsFatal(%v) = %v, want %v", e, got, want)
		}
	}
}

// fakeAPI is a Hetzner project in memory. stock[n] is what the n-th look at the stock shows (the last one
// repeats); perCheck limits how many creates succeed per look, like stock running out.
type fakeAPI struct {
	stock    [][]*hcloud.ServerType
	looks    int
	perCheck int // 0: no limit
	made     int // creates that succeeded in this look
	servers  []*hcloud.Server
	creates  []string // "type location" of each create tried
	failWith map[string]error
	// lost: the create of this name goes through, but the caller gets this error instead of the reply.
	lost        map[string]error
	lookupErr   error
	lookupFails int // the next lookups that fail
}

func (f *fakeAPI) ServerTypes(context.Context) ([]*hcloud.ServerType, error) {
	t := f.stock[min(f.looks, len(f.stock)-1)]
	f.looks++
	f.made = 0
	return t, nil
}

func (f *fakeAPI) Prices(context.Context) (cloud.Prices, error) {
	return cloud.Prices{Currency: "EUR", IPv4: ipv4}, nil
}

func (f *fakeAPI) Managed(context.Context) ([]*hcloud.Server, error) { return f.servers, nil }

func (f *fakeAPI) ServerByName(_ context.Context, name string) (*hcloud.Server, error) {
	if f.lookupErr != nil {
		return nil, f.lookupErr
	}
	if f.lookupFails > 0 {
		f.lookupFails--
		return nil, errors.New("timeout")
	}
	for _, s := range f.servers {
		if s.Name == name {
			return s, nil
		}
	}
	return nil, nil
}

func (f *fakeAPI) Create(_ context.Context, o hcloud.ServerCreateOpts) (hcloud.ServerCreateResult, error) {
	k := o.ServerType.Name + " " + o.Location.Name
	f.creates = append(f.creates, k)
	if err := f.failWith[k]; err != nil {
		return hcloud.ServerCreateResult{}, err
	}
	for _, s := range f.servers {
		if s.Name == o.Name {
			return hcloud.ServerCreateResult{}, apiErr("uniqueness_error", "server name is already used")
		}
	}
	if f.perCheck > 0 && f.made == f.perCheck {
		return hcloud.ServerCreateResult{}, apiErr("resource_unavailable", "sold out")
	}
	f.made++
	// Like Hetzner: the server has its IPv4 at once; backups come later, so no backup window yet.
	s := &hcloud.Server{
		ID: int64(len(f.servers) + 1), Name: o.Name, Labels: o.Labels, ServerType: o.ServerType,
		Location: &hcloud.Location{Name: o.Location.Name},
	}
	s.PublicNet.IPv4.IP = net.ParseIP("203.0.113.9")
	f.servers = append(f.servers, s)
	if err := f.lost[o.Name]; err != nil {
		delete(f.lost, o.Name)
		return hcloud.ServerCreateResult{}, err
	}
	return hcloud.ServerCreateResult{Server: s, Action: &hcloud.Action{ID: 1}}, nil
}

func newBuyer(api API, w Wants, n int) (*Buyer, *int) {
	sleeps := 0
	b := &Buyer{
		API: api, Wants: w, Interval: time.Minute,
		Sleep: func(context.Context, time.Duration) error { sleeps++; return nil },
	}
	for i := 1; i <= n; i++ {
		name := "vps"
		if n > 1 {
			name = "vps-" + string(rune('0'+i))
		}
		b.Slots = append(b.Slots, hcloud.ServerCreateOpts{Name: name, Labels: cloud.Labels()})
	}
	return b, &sleeps
}

// realTotal is what the servers really cost once setup is done, backups included when on.
func realTotal(servers []*hcloud.Server, backups bool) float64 {
	var sum float64
	for _, s := range servers {
		p, _ := Monthly(s.ServerType, s.Location.Name, ipv4, backups)
		sum += p
	}
	return sum
}

var nbg = []string{"nbg1"}

func inStock(name, price string) *hcloud.ServerType {
	return st(name, hcloud.CPUTypeShared, "x86", map[string]string{"nbg1": price}, map[string]bool{"nbg1": true})
}

func TestBuyerWaitsForStock(t *testing.T) {
	sold := st("cx23", hcloud.CPUTypeShared, "x86", map[string]string{"nbg1": "4"}, nil)
	api := &fakeAPI{stock: [][]*hcloud.ServerType{{sold}, {sold}, {inStock("cx23", "4")}}}
	b, sleeps := newBuyer(api, Wants{Locations: nbg, MaxPrice: 10}, 1)
	got, err := b.Run(context.Background())
	if err != nil || len(got) != 1 || *sleeps != 2 || len(api.creates) != 1 {
		t.Fatalf("got %v err %v after %d sleeps, creates %v", got, err, *sleeps, api.creates)
	}
}

func TestBuyerRetriesRefusalsAndStopsOnFatal(t *testing.T) {
	api := &fakeAPI{
		stock:    [][]*hcloud.ServerType{{inStock("cx23", "4")}},
		failWith: map[string]error{"cx23 nbg1": apiErr("resource_unavailable", "sold out")},
	}
	b, sleeps := newBuyer(api, Wants{Locations: nbg, MaxPrice: 10}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	b.Sleep = func(context.Context, time.Duration) error {
		if *sleeps++; *sleeps == 3 {
			cancel()
			return ctx.Err()
		}
		return nil
	}
	if _, err := b.Run(ctx); !errors.Is(err, context.Canceled) || len(api.creates) != 3 {
		t.Fatalf("err %v, creates %v", err, api.creates)
	}

	api.failWith["cx23 nbg1"] = apiErr("unauthorized", "bad token")
	b, _ = newBuyer(api, Wants{Locations: nbg, MaxPrice: 10}, 1)
	if _, err := b.Run(context.Background()); !cloud.IsCode(err, "unauthorized") {
		t.Fatalf("want the unauthorized error, got %v", err)
	}
}

func TestBuyerBuysSeveralAcrossChecks(t *testing.T) {
	api := &fakeAPI{stock: [][]*hcloud.ServerType{{inStock("cx23", "4")}}, perCheck: 1}
	var bought []int
	b, sleeps := newBuyer(api, Wants{Locations: nbg, MaxPrice: 10}, 3)
	b.Bought = func(_ Purchase, n int) { bought = append(bought, n) }
	got, err := b.Run(context.Background())
	if err != nil || len(got) != 3 || *sleeps != 2 || len(bought) != 3 || bought[2] != 3 {
		t.Fatalf("got %d, err %v, sleeps %d, bought %v", len(got), err, *sleeps, bought)
	}
}

func TestBuyerTotalCap(t *testing.T) {
	api := &fakeAPI{stock: [][]*hcloud.ServerType{{inStock("cx23", "4")}}}
	b, _ := newBuyer(api, Wants{Locations: nbg, MaxPrice: 10}, 3)
	b.MaxTotal = 10 // two at 4.50, not three
	got, err := b.Run(context.Background())
	if !errors.Is(err, ErrOverTotal) || len(got) != 2 {
		t.Fatalf("got %d, err %v", len(got), err)
	}
}

// Backups are turned on only after buying, so servers bought in an earlier check have no backup window
// yet. They must still count with backups, or the cap is passed: three at 5.30 would be 15.90.
func TestBuyerCountsBackupsOfThisRun(t *testing.T) {
	api := &fakeAPI{stock: [][]*hcloud.ServerType{{inStock("cx23", "4")}}, perCheck: 1}
	b, _ := newBuyer(api, Wants{Locations: nbg, Backups: true}, 3)
	b.MaxTotal = 15
	got, err := b.Run(context.Background())
	if total := realTotal(api.servers, true); total > 15 || !errors.Is(err, ErrOverTotal) {
		t.Fatalf("bought %d costing %.2f/month with backups, over --max-total 15 (err %v)", len(got), total, err)
	}
}

// A create that went through but whose reply was lost is found by name at once, with what it really is:
// here a cx33 at 6.50, so a second cx33 no longer fits under 12 and the cx23 is bought instead.
func TestBuyerLostReplyKeepsRealPrice(t *testing.T) {
	api := &fakeAPI{
		stock: [][]*hcloud.ServerType{{inStock("cx33", "6"), inStock("cx23", "4")}},
		lost:  map[string]error{"vps-1": errors.New("context deadline exceeded (Client.Timeout exceeded while awaiting headers)")},
	}
	b, _ := newBuyer(api, Wants{Types: []string{"cx33", "cx23"}, Locations: nbg, MaxPrice: 10}, 2)
	b.MaxTotal = 12
	got, err := b.Run(context.Background())
	if err != nil || len(got) != 2 {
		t.Fatalf("got %d, err %v", len(got), err)
	}
	if got[0].Candidate.Type != "cx33" || got[0].Candidate.Price != 6.5 || got[0].Action != nil {
		t.Errorf("found server recorded as %+v", got[0].Candidate)
	}
	if total := realTotal(api.servers, false); total > 12 {
		t.Fatalf("real total %.2f over 12", total)
	}
	if got := strings.Join(api.creates, ","); got != "cx33 nbg1,cx23 nbg1" {
		t.Fatalf("after the lost reply it should look vps-1 up, not try it again: creates %s", got)
	}
}

// When even the lookup fails, the next candidate's create says the name is taken. The server found then is
// the cx33 from before, and must be recorded as that, not as the cx23 being tried.
func TestBuyerFoundLaterKeepsRealType(t *testing.T) {
	api := &fakeAPI{
		stock:       [][]*hcloud.ServerType{{inStock("cx33", "6"), inStock("cx23", "4")}},
		lost:        map[string]error{"vps": errors.New("connection reset")},
		lookupFails: 1,
	}
	b, _ := newBuyer(api, Wants{Types: []string{"cx33", "cx23"}, Locations: nbg, MaxPrice: 10}, 1)
	got, err := b.Run(context.Background())
	if err != nil || len(got) != 1 || got[0].Candidate.Type != "cx33" || got[0].Candidate.Price != 6.5 {
		t.Fatalf("got %+v, err %v", got, err)
	}
}

// Hetzner's own retry of a timed-out create gets "name already used" for the server it just made.
func TestBuyerAdoptsOnUniquenessError(t *testing.T) {
	api := &fakeAPI{stock: [][]*hcloud.ServerType{{inStock("cx23", "4")}}}
	api.servers = []*hcloud.Server{{Name: "vps", Labels: cloud.Labels(), ServerType: inStock("cx23", "4"), Location: &hcloud.Location{Name: "nbg1"}}}
	b, _ := newBuyer(api, Wants{Locations: nbg, MaxPrice: 10}, 1)
	got, err := b.Run(context.Background())
	if err != nil || len(got) != 1 || got[0].Candidate.Price != 4.5 {
		t.Fatalf("got %v, err %v", got, err)
	}

	api.servers[0].Labels = nil // someone else's server with that name: not ours to adopt
	b, _ = newBuyer(api, Wants{Locations: nbg, MaxPrice: 10}, 1)
	if _, err := b.Run(context.Background()); !cloud.IsCode(err, "uniqueness_error") {
		t.Fatalf("want uniqueness_error, got %v", err)
	}
}

func TestRecover(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	api := &fakeAPI{
		stock: [][]*hcloud.ServerType{{inStock("cx23", "4")}},
		lost:  map[string]error{"vps": context.Canceled},
	}
	b, _ := newBuyer(api, Wants{Locations: nbg, MaxPrice: 10}, 1)
	cancel() // Ctrl+C while the create was on its way
	got, err := b.Run(ctx)
	if len(got) != 0 || err == nil {
		t.Fatalf("got %v, err %v", got, err)
	}
	found, unknown := b.Recover(ctx)
	if len(found) != 1 || found[0].Server.Name != "vps" || found[0].Candidate.Price != 4.5 || len(unknown) != 0 {
		t.Fatalf("found %v, unknown %v", found, unknown)
	}

	api.lookupErr = errors.New("bad gateway")
	b.got = nil
	if found, unknown := b.Recover(context.Background()); len(found) != 0 || strings.Join(unknown, ",") != "vps" {
		t.Fatalf("a failed lookup must come back as unknown, got found %v unknown %v", found, unknown)
	}
}
