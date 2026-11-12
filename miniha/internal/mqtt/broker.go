/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package mqtt

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"

	mochi "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/mochi-mqtt/server/v2/packets"
)

type BrokerConfig struct {
	Listen string            // e.g. ":1883"
	Users  map[string]string // username -> password; empty allows anonymous

	// TLS listener next to the plain one; all three or none.
	TLSListen      string
	TLSCertificate string
	TLSKey         string

	// DataDir enables persistence of retained messages under
	// <DataDir>/broker/retained.json; empty keeps them in RAM only.
	DataDir string
}

// Broker is the embedded MQTT broker. The entities talk to it through
// its inline client, so no network round trip and no second process.
type Broker struct {
	srv *mochi.Server
	cfg BrokerConfig
	log *slog.Logger

	mu     sync.Mutex
	subs   map[string]*subEntry
	nextID int
}

type subEntry struct {
	id     int
	once   sync.Once
	nextCB int
	cbs    map[int]func(Message)
}

func NewBroker(cfg BrokerConfig, log *slog.Logger) (*Broker, error) {
	if cfg.Listen == "" {
		return nil, fmt.Errorf("mqtt listen address must be set")
	}
	opts := &mochi.Options{InlineClient: true, Logger: log.With("component", "broker")}
	srv := mochi.New(opts)
	if len(cfg.Users) == 0 {
		if err := srv.AddHook(new(auth.AllowHook), nil); err != nil {
			return nil, err
		}
	} else {
		ledger := &auth.Ledger{}
		for u, p := range cfg.Users {
			ledger.Auth = append(ledger.Auth, auth.AuthRule{Username: auth.RString(u), Password: auth.RString(p), Allow: true})
		}
		// Inline (local) publishes bypass auth; everything else needs a user.
		ledger.ACL = auth.ACLRules{{Filters: auth.Filters{"#": auth.ReadWrite}}}
		if err := srv.AddHook(new(auth.Hook), &auth.Options{Ledger: ledger}); err != nil {
			return nil, err
		}
	}
	if cfg.DataDir != "" {
		if err := srv.AddHook(newRetainedHook(filepath.Join(cfg.DataDir, "broker", "retained.json")), nil); err != nil {
			return nil, err
		}
	}
	if err := srv.AddListener(listeners.NewTCP(listeners.Config{ID: "tcp", Address: cfg.Listen})); err != nil {
		return nil, err
	}
	if cfg.TLSListen != "" {
		if cfg.TLSCertificate == "" || cfg.TLSKey == "" {
			return nil, fmt.Errorf("mqtt TLS listener needs a certificate and a key")
		}
		pair, err := tls.LoadX509KeyPair(cfg.TLSCertificate, cfg.TLSKey)
		if err != nil {
			return nil, fmt.Errorf("mqtt TLS certificate: %w", err)
		}
		tlsCfg := &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
		if err := srv.AddListener(listeners.NewTCP(listeners.Config{ID: "tls", Address: cfg.TLSListen, TLSConfig: tlsCfg})); err != nil {
			return nil, err
		}
	} else if cfg.TLSCertificate != "" || cfg.TLSKey != "" {
		return nil, fmt.Errorf("mqtt TLS certificate given without a TLS listen address")
	}
	return &Broker{srv: srv, cfg: cfg, log: log, subs: map[string]*subEntry{}}, nil
}

func (b *Broker) Start() error {
	if len(b.cfg.Users) == 0 {
		b.log.Warn("MQTT broker accepts anonymous clients; set mqtt.users in configuration.yaml to require credentials")
	}
	return b.srv.Serve()
}

func (b *Broker) Stop() error { return b.srv.Close() }

// Subscribe multiplexes callbacks per filter over one inline subscription,
// because the broker allows one handler per (filter, id).
func (b *Broker) Subscribe(filter string, cb func(Message)) (func(), error) {
	b.mu.Lock()
	entry := b.subs[filter]
	if entry == nil {
		b.nextID++
		entry = &subEntry{id: b.nextID, cbs: map[int]func(Message){}}
		b.subs[filter] = entry
	}
	entry.nextCB++
	cbID := entry.nextCB
	entry.cbs[cbID] = cb
	b.mu.Unlock()
	// The broker delivers retained messages synchronously from Subscribe,
	// and the handler takes b.mu, so the registration happens unlocked.
	var regErr error
	entry.once.Do(func() {
		handler := func(_ *mochi.Client, _ packets.Subscription, pk packets.Packet) {
			b.mu.Lock()
			cbs := make([]func(Message), 0, len(entry.cbs))
			for _, f := range entry.cbs {
				cbs = append(cbs, f)
			}
			b.mu.Unlock()
			m := Message{Topic: pk.TopicName, Payload: pk.Payload, Retained: pk.FixedHeader.Retain}
			for _, f := range cbs {
				f(m)
			}
		}
		regErr = b.srv.Subscribe(filter, entry.id, handler)
	})
	if regErr != nil {
		b.mu.Lock()
		delete(entry.cbs, cbID)
		if len(entry.cbs) == 0 {
			delete(b.subs, filter)
		}
		b.mu.Unlock()
		return nil, regErr
	}
	return func() {
		b.mu.Lock()
		delete(entry.cbs, cbID)
		last := len(entry.cbs) == 0 && b.subs[filter] == entry
		if last {
			delete(b.subs, filter)
		}
		b.mu.Unlock()
		if last {
			b.srv.Unsubscribe(filter, entry.id)
		}
	}, nil
}

func (b *Broker) Publish(topic string, payload []byte, qos byte, retain bool) error {
	return b.srv.Publish(topic, payload, retain, qos)
}

// ClientCount reports connected clients, for the About page.
func (b *Broker) ClientCount() int { return b.srv.Clients.Len() }
