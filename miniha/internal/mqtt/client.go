/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package mqtt

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
)

// ClientConfig describes an external broker to use instead of the
// embedded one.
type ClientConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	TLS      bool
	ClientID string
	Will     *Will
	// MaxSubscriptions caps what is subscribed at the broker; 0 means
	// one subscription per filter. Hosted brokers often allow only a
	// handful per connection, and a "quota exceeded" answer sets it
	// automatically.
	MaxSubscriptions int
	// OnConnect runs after every (re)connection once subscriptions are
	// restored; the discovery uses it to resend the birth message.
	OnConnect func()
}

// Client is the Transport for an external broker. Subscriptions are kept
// locally and replayed on every reconnect.
type Client struct {
	cfg ClientConfig
	log *slog.Logger
	cm  *autopaho.ConnectionManager

	connected atomic.Bool

	mu     sync.Mutex
	subs   map[string]*subEntry // exact filters wanted by entities
	remote map[string]bool      // filters acknowledged by the broker
	cap    int
	cancel context.CancelFunc
	// Incoming messages are handed to callbacks from this queue, never
	// from paho's receive goroutine: a callback that subscribes (every
	// newly discovered entity does) would otherwise wait for a SUBACK
	// that only the blocked receiver could read.
	inbox chan Message
	// Subscribes and publishes leave through one ordered worker, so a
	// state request sent right after an entity subscribed reaches the
	// broker after that subscription, whatever the goroutine scheduling.
	outbox chan func()
}

func NewClient(cfg ClientConfig, log *slog.Logger) (*Client, error) {
	if cfg.Host == "" {
		return nil, fmt.Errorf("external broker host must be set")
	}
	if cfg.Port == 0 {
		cfg.Port = 1883
		if cfg.TLS {
			cfg.Port = 8883
		}
	}
	if cfg.ClientID == "" {
		// Two instances with the same client id make the broker kick
		// each other off in turns, so the default is unique per host
		// and process.
		host, _ := os.Hostname()
		raw := make([]byte, 3)
		rand.Read(raw)
		cfg.ClientID = fmt.Sprintf("miniha-%s-%x", host, raw)
	}
	return &Client{cfg: cfg, log: log, subs: map[string]*subEntry{}, remote: map[string]bool{}, cap: cfg.MaxSubscriptions,
		inbox: make(chan Message, 4096), outbox: make(chan func(), 4096)}, nil
}

// planRemote decides which filters to subscribe at the broker for the
// wanted exact filters under a cap: unlimited means exact; otherwise one
// "<first level>/#" per first topic level, and "#" when even that is
// too many. Local dispatch always matches the exact filters, so a
// broader remote subscription only costs traffic, never correctness.
func planRemote(wanted []string, cap int) []string {
	if cap <= 0 {
		return wanted
	}
	set := map[string]bool{}
	for _, f := range wanted {
		first, _, _ := strings.Cut(f, "/")
		if first == "" || strings.ContainsAny(first, "+#") {
			set["#"] = true
			continue
		}
		set[first+"/#"] = true
	}
	if set["#"] || len(set) > cap {
		return []string{"#"}
	}
	out := make([]string, 0, len(set))
	for f := range set {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// reconcile brings the broker-side subscriptions in line with the plan;
// it runs on the outbox worker, one step at a time.
func (c *Client) reconcile() {
	if !c.connected.Load() {
		return
	}
	c.mu.Lock()
	wanted := make([]string, 0, len(c.subs))
	for f := range c.subs {
		wanted = append(wanted, f)
	}
	desired := map[string]bool{}
	for _, f := range planRemote(wanted, c.cap) {
		desired[f] = true
	}
	var toAdd, toDrop []string
	for f := range desired {
		if !c.remote[f] {
			toAdd = append(toAdd, f)
		}
	}
	for f := range c.remote {
		if !desired[f] {
			toDrop = append(toDrop, f)
		}
	}
	c.mu.Unlock()
	sort.Strings(toAdd)
	// Subscribe before unsubscribing so no topic goes dark in between.
	for _, f := range toAdd {
		err := c.subscribeRemote(f)
		if err == nil {
			c.mu.Lock()
			c.remote[f] = true
			c.mu.Unlock()
			continue
		}
		if quota := errors.Is(err, errQuota); quota {
			c.mu.Lock()
			c.cap = max(1, len(c.remote))
			cap := c.cap
			c.mu.Unlock()
			c.log.Warn("broker limits subscriptions per connection; consolidating", "host", c.cfg.Host, "max_subscriptions", cap)
			c.outbox <- c.reconcile
			return
		}
		c.log.Warn("subscribe failed, retrying", "filter", f, "err", err)
		time.AfterFunc(5*time.Second, func() { c.outbox <- c.reconcile })
		return
	}
	for _, f := range toDrop {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := c.cm.Unsubscribe(ctx, &paho.Unsubscribe{Topics: []string{f}})
		cancel()
		if err != nil {
			c.log.Warn("unsubscribe failed", "filter", f, "err", err)
		}
		c.mu.Lock()
		delete(c.remote, f)
		c.mu.Unlock()
	}
}

var errQuota = errors.New("quota exceeded")

func (c *Client) send(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-c.outbox:
			job()
		}
	}
}

func (c *Client) dispatch(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-c.inbox:
			c.mu.Lock()
			var cbs []func(Message)
			for f, e := range c.subs {
				if MatchTopic(f, m.Topic) {
					for _, cb := range e.cbs {
						cbs = append(cbs, cb)
					}
				}
			}
			c.mu.Unlock()
			for _, cb := range cbs {
				cb(m)
			}
		}
	}
}

// SetOnConnect installs the reconnect hook; it must be set before Start.
func (c *Client) SetOnConnect(fn func()) {
	c.mu.Lock()
	c.cfg.OnConnect = fn
	c.mu.Unlock()
}

// Port is the effective port after defaults were applied.
func (c *Client) Port() int { return c.cfg.Port }

// Start begins connecting and returns once the first attempt succeeded
// or a short grace period passed: an unreachable broker must not keep
// the hub from serving the others. autopaho keeps retrying.
func (c *Client) Start() error {
	scheme := "mqtt"
	if c.cfg.TLS {
		scheme = "mqtts"
	}
	u, err := url.Parse(fmt.Sprintf("%s://%s:%d", scheme, c.cfg.Host, c.cfg.Port))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	go c.dispatch(ctx)
	go c.send(ctx)
	acfg := autopaho.ClientConfig{
		ServerUrls:                    []*url.URL{u},
		KeepAlive:                     30,
		CleanStartOnInitialConnection: true,
		SessionExpiryInterval:         0,
		ConnectRetryDelay:             5 * time.Second,
		ConnectUsername:               c.cfg.Username,
		ConnectPassword:               []byte(c.cfg.Password),
		OnConnectionUp:                c.onUp,
		OnConnectError: func(err error) {
			c.connected.Store(false)
			c.log.Warn("external broker connection failed", "host", c.cfg.Host, "err", err)
		},
		ClientConfig: paho.ClientConfig{
			ClientID:          c.cfg.ClientID,
			OnPublishReceived: []func(paho.PublishReceived) (bool, error){c.onPublish},
			OnServerDisconnect: func(d *paho.Disconnect) {
				c.connected.Store(false)
				c.log.Warn("external broker disconnected", "host", c.cfg.Host, "reason", d.ReasonCode)
			},
			OnClientError: func(err error) {
				c.connected.Store(false)
				c.log.Warn("external broker client error", "host", c.cfg.Host, "err", err)
			},
		},
	}
	if c.cfg.TLS {
		acfg.TlsCfg = &tls.Config{ServerName: c.cfg.Host}
	}
	if w := c.cfg.Will; w != nil {
		acfg.WillMessage = &paho.WillMessage{Topic: w.Topic, Payload: []byte(w.Payload), QoS: w.QoS, Retain: w.Retain}
	}
	cm, err := autopaho.NewConnection(ctx, acfg)
	if err != nil {
		cancel()
		return err
	}
	c.cm = cm
	waitCtx, waitCancel := context.WithTimeout(ctx, 5*time.Second)
	defer waitCancel()
	if err := cm.AwaitConnection(waitCtx); err != nil {
		c.log.Warn("external broker not reachable yet; retrying in the background", "host", c.cfg.Host, "port", c.cfg.Port)
	}
	return nil
}

// Connected reports the current link state, for the status page.
func (c *Client) Connected() bool { return c.connected.Load() }

func (c *Client) Stop() error {
	if c.cm == nil {
		return nil
	}
	// Let queued work (the will message among it) go out first.
	drained := make(chan struct{})
	select {
	case c.outbox <- func() { close(drained) }:
		select {
		case <-drained:
		case <-time.After(3 * time.Second):
		}
	default:
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := c.cm.Disconnect(ctx)
	c.cancel()
	return err
}

func (c *Client) onUp(cm *autopaho.ConnectionManager, _ *paho.Connack) {
	c.connected.Store(true)
	c.log.Info("external broker connected", "host", c.cfg.Host, "port", c.cfg.Port)
	// A new session has no subscriptions; replay through the planner.
	c.mu.Lock()
	c.remote = map[string]bool{}
	c.mu.Unlock()
	c.outbox <- c.reconcile
	c.mu.Lock()
	hook := c.cfg.OnConnect
	c.mu.Unlock()
	if hook != nil {
		hook()
	}
}

func (c *Client) subscribeRemote(filter string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ack, err := c.cm.Subscribe(ctx, &paho.Subscribe{Subscriptions: []paho.SubscribeOptions{{Topic: filter, QoS: 0}}})
	if err != nil && ack != nil && len(ack.Reasons) > 0 {
		if ack.Reasons[0] == 0x97 {
			return fmt.Errorf("%w: %v", errQuota, err)
		}
		return fmt.Errorf("%w (reason code 0x%02x)", err, ack.Reasons[0])
	}
	return err
}

func (c *Client) onPublish(pr paho.PublishReceived) (bool, error) {
	c.inbox <- Message{Topic: pr.Packet.Topic, Payload: pr.Packet.Payload, Retained: pr.Packet.Retain}
	return true, nil
}

func (c *Client) Subscribe(filter string, cb func(Message)) (func(), error) {
	c.mu.Lock()
	entry := c.subs[filter]
	fresh := entry == nil
	if fresh {
		entry = &subEntry{cbs: map[int]func(Message){}}
		c.subs[filter] = entry
	}
	entry.nextCB++
	id := entry.nextCB
	entry.cbs[id] = cb
	c.mu.Unlock()
	if fresh && c.cm != nil {
		// Queued, not awaited: the caller may be the dispatcher, and the
		// broker's reply must not be awaited from there.
		c.outbox <- c.reconcile
	}
	return func() {
		c.mu.Lock()
		delete(entry.cbs, id)
		last := len(entry.cbs) == 0 && c.subs[filter] == entry
		if last {
			delete(c.subs, filter)
		}
		c.mu.Unlock()
		if last && c.cm != nil {
			c.outbox <- c.reconcile
		}
	}, nil
}

// Publish is queued behind earlier subscribes; failures are logged
// because the caller has already moved on.
func (c *Client) Publish(topic string, payload []byte, qos byte, retain bool) error {
	c.outbox <- func() {
		if !c.connected.Load() {
			c.log.Warn("publish dropped, broker not connected", "host", c.cfg.Host, "topic", topic)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := c.cm.Publish(ctx, &paho.Publish{Topic: topic, Payload: payload, QoS: qos, Retain: retain}); err != nil {
			c.log.Error("publish failed", "topic", topic, "err", err)
		}
	}
	return nil
}
