/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

// Package netdiscovery browses the LAN with mDNS and lists devices that are
// not integrated yet, each with a hint how to connect it to MiniHA. It does
// not integrate anything itself: MiniHA's only device protocol is MQTT.
package netdiscovery

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/grandcat/zeroconf"
)

// DefaultInterval is how often the network is browsed.
const DefaultInterval = 5 * time.Minute

// browseTimeout is how long one browse round listens for answers.
const browseTimeout = 10 * time.Second

// expireIntervals is how many browse intervals a device may stay silent
// before it is dropped; one missed round must not make a device flicker.
const expireIntervals = 3

// DefaultServices are the mDNS service types browsed when Options.Services
// is empty.
var DefaultServices = []string{
	"_esphomelib._tcp",
	"_shelly._tcp",
	"_http._tcp",
	"_hap._tcp",
	"_googlecast._tcp",
	"_home-assistant._tcp",
	"_mqtt._tcp",
	"_secure-mqtt._tcp",
	"_printer._tcp",
	"_ipp._tcp",
}

// Device is one service instance found on the network.
type Device struct {
	Name       string
	Host       string
	Addrs      []string
	Port       int
	Service    string // e.g. "_esphomelib._tcp"
	Kind       string // see the Kind constants
	Model      string // from TXT when known
	TXT        map[string]string
	Hint       string
	Integrated bool // set by Options.IsIntegrated when listing
	FirstSeen  time.Time
	LastSeen   time.Time
}

// Options configure a Browser.
type Options struct {
	// Interval between browse rounds; DefaultInterval when zero.
	Interval time.Duration
	// Services are the mDNS service types to browse; DefaultServices when empty.
	Services []string
	// MQTTHost is the address devices should point their MQTT client at; it
	// only appears in hints.
	MQTTHost string
	// IsIntegrated tells whether a device is already known to MiniHA. Nil
	// means no device is.
	IsIntegrated func(d Device) bool
}

// entry is the part of a zeroconf.ServiceEntry the browser uses. Tests build
// these directly so they need no network.
type entry struct {
	Instance string
	Service  string
	Host     string
	Port     int
	Text     []string
	Addrs    []string
}

// fromZeroconf converts a resolver result. The service type is the one that
// was browsed, because the resolver may report it with a domain suffix.
func fromZeroconf(service string, e *zeroconf.ServiceEntry) entry {
	out := entry{
		Instance: e.Instance,
		Service:  service,
		Host:     e.HostName,
		Port:     e.Port,
		Text:     e.Text,
	}
	for _, ip := range e.AddrIPv4 {
		out.Addrs = append(out.Addrs, ip.String())
	}
	for _, ip := range e.AddrIPv6 {
		out.Addrs = append(out.Addrs, ip.String())
	}
	return out
}

// parseTXT splits "key=value" strings; a key without "=" maps to "" as in
// RFC 6763 boolean attributes.
func parseTXT(text []string) map[string]string {
	txt := make(map[string]string, len(text))
	for _, kv := range text {
		k, v, _ := strings.Cut(kv, "=")
		k = strings.ToLower(k)
		if k == "" {
			continue
		}
		if _, dup := txt[k]; dup {
			// RFC 6763 6.4: the first occurrence wins.
			continue
		}
		txt[k] = v
	}
	return txt
}

// Browser periodically browses mDNS and keeps the devices it saw.
type Browser struct {
	log  *slog.Logger
	opts Options

	mu      sync.Mutex
	devices map[string]*Device
	cancel  context.CancelFunc
	done    chan struct{}
}

// New returns a stopped Browser.
func New(log *slog.Logger, opts Options) *Browser {
	if log == nil {
		log = slog.Default()
	}
	if opts.Interval <= 0 {
		opts.Interval = DefaultInterval
	}
	if len(opts.Services) == 0 {
		opts.Services = DefaultServices
	}
	return &Browser{log: log, opts: opts, devices: map[string]*Device{}}
}

// Start begins browsing in the background; a second call is a no-op.
func (b *Browser) Start() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	b.cancel = cancel
	b.done = make(chan struct{})
	go b.run(ctx, b.done)
}

// Stop ends browsing and waits for the background goroutine.
func (b *Browser) Stop() {
	b.mu.Lock()
	cancel, done := b.cancel, b.done
	b.cancel, b.done = nil, nil
	b.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
}

func (b *Browser) run(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(b.opts.Interval)
	defer ticker.Stop()
	for {
		b.scan(ctx)
		b.expire(time.Now())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// scan browses every service type once and merges what answers within
// browseTimeout.
func (b *Browser) scan(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, browseTimeout)
	defer cancel()

	var wg sync.WaitGroup
	for _, service := range b.opts.Services {
		// One resolver per query: concurrent Browse calls on a shared
		// resolver read the same sockets and swallow each other's answers.
		resolver, err := zeroconf.NewResolver(zeroconf.SelectIPTraffic(zeroconf.IPv4))
		if err != nil {
			b.log.Error("mDNS resolver failed", "err", err)
			return
		}
		// Browse closes the channel when the context ends or the query
		// fails, which ends the reader below.
		results := make(chan *zeroconf.ServiceEntry, 16)
		if err := resolver.Browse(ctx, service, "local.", results); err != nil {
			b.log.Error("mDNS browse failed", "service", service, "err", err)
			continue
		}
		wg.Add(1)
		go func(service string) {
			defer wg.Done()
			for e := range results {
				b.merge([]entry{fromZeroconf(service, e)}, time.Now())
			}
		}(service)
	}
	wg.Wait()
}

// merge adds or refreshes devices. FirstSeen survives a refresh.
func (b *Browser) merge(entries []entry, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, e := range entries {
		service := normalizeService(e.Service)
		txt := parseTXT(e.Text)
		kind, model, hint := Hint(service, txt, b.opts.MQTTHost)
		if service == "_http._tcp" && kind == KindOther {
			// Every web server announces _http._tcp; only Tasmota is of interest.
			continue
		}
		name := e.Instance
		if kind == KindCast && txt["fn"] != "" {
			name = txt["fn"]
		}
		key := service + "/" + e.Instance
		d := &Device{
			Name:      name,
			Host:      strings.TrimSuffix(e.Host, "."),
			Addrs:     append([]string(nil), e.Addrs...),
			Port:      e.Port,
			Service:   service,
			Kind:      kind,
			Model:     model,
			TXT:       txt,
			Hint:      hint,
			FirstSeen: now,
			LastSeen:  now,
		}
		if old, ok := b.devices[key]; ok {
			d.FirstSeen = old.FirstSeen
		}
		b.devices[key] = d
	}
}

// expire drops devices not seen for expireIntervals browse intervals.
func (b *Browser) expire(now time.Time) {
	limit := expireIntervals * b.opts.Interval
	b.mu.Lock()
	defer b.mu.Unlock()
	for key, d := range b.devices {
		if now.Sub(d.LastSeen) > limit {
			delete(b.devices, key)
		}
	}
}

// Devices returns a copy of the known devices sorted by name, then service
// and host so the order is stable.
func (b *Browser) Devices() []Device {
	b.mu.Lock()
	out := make([]Device, 0, len(b.devices))
	for _, d := range b.devices {
		c := *d
		c.Addrs = append([]string(nil), d.Addrs...)
		c.TXT = make(map[string]string, len(d.TXT))
		for k, v := range d.TXT {
			c.TXT[k] = v
		}
		out = append(out, c)
	}
	b.mu.Unlock()

	// The callback runs outside the lock because it may call into other
	// components that take their own locks.
	if b.opts.IsIntegrated != nil {
		for i := range out {
			out[i].Integrated = b.opts.IsIntegrated(out[i])
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, c := out[i], out[j]
		if a.Name != c.Name {
			return a.Name < c.Name
		}
		if a.Service != c.Service {
			return a.Service < c.Service
		}
		return a.Host < c.Host
	})
	return out
}

// List implements api.DiscoverySource.
func (b *Browser) List() []map[string]any {
	devices := b.Devices()
	out := make([]map[string]any, 0, len(devices))
	for _, d := range devices {
		out = append(out, map[string]any{
			"name":       d.Name,
			"host":       d.Host,
			"addrs":      d.Addrs,
			"port":       d.Port,
			"service":    d.Service,
			"kind":       d.Kind,
			"model":      d.Model,
			"txt":        d.TXT,
			"hint":       d.Hint,
			"integrated": d.Integrated,
			"first_seen": d.FirstSeen.UTC().Format(time.RFC3339),
			"last_seen":  d.LastSeen.UTC().Format(time.RFC3339),
		})
	}
	return out
}
