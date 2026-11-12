/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package mqtt

import (
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/probonopd/miniha/internal/core"
)

// The embedded broker stands in for an external one; the paho client
// must deliver discovery and state through it like the inline path does.
func TestPlanRemote(t *testing.T) {
	wanted := []string{"homeassistant/+/+/config", "homeassistant/+/+/+/config", "tasmota/discovery/+/config", "tv/echoshow/state", "tv/yellow/state", "minitv-1/config"}
	if got := planRemote(wanted, 0); len(got) != len(wanted) {
		t.Fatalf("unlimited must stay exact: %v", got)
	}
	if got := planRemote(wanted, 10); strings.Join(got, " ") != "homeassistant/# minitv-1/# tasmota/# tv/#" {
		t.Fatalf("grouped: %v", got)
	}
	if got := planRemote(wanted, 3); strings.Join(got, " ") != "#" {
		t.Fatalf("over cap: %v", got)
	}
	if got := planRemote([]string{"+/status", "a/b"}, 5); strings.Join(got, " ") != "#" {
		t.Fatalf("wildcard first level: %v", got)
	}
}

func TestExternalClient(t *testing.T) {
	for _, maxSubs := range []int{0, 2} {
		t.Run(fmt.Sprintf("max_subscriptions=%d", maxSubs), func(t *testing.T) { externalClientRoundTrip(t, maxSubs) })
	}
}

func externalClientRoundTrip(t *testing.T, maxSubs int) {
	addr := freePort(t)
	b, err := NewBroker(BrokerConfig{Listen: addr}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Start(); err != nil {
		t.Fatal(err)
	}
	defer b.Stop()
	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)
	connected := make(chan struct{}, 2)
	cl, err := NewClient(ClientConfig{Host: host, Port: port, MaxSubscriptions: maxSubs, OnConnect: func() { connected <- struct{}{} }}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if err := cl.Start(); err != nil {
		t.Fatal(err)
	}
	defer cl.Stop()
	<-connected
	c := core.New()
	d := NewDiscovery(c, cl, Options{Prefix: "homeassistant"}, slog.Default())
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	defer d.Stop()
	// Published through the broker's inline client, i.e. from "outside".
	b.Publish("homeassistant/switch/s1/config", []byte(`{"name":"S","cmd_t":"s1/set","stat_t":"s1/state","uniq_id":"s1"}`), 0, true)
	time.Sleep(100 * time.Millisecond)
	b.Publish("s1/state", []byte("ON"), 0, false)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s := c.GetState("switch.s"); s != nil && s.State == "on" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("state not delivered via external client: %v", ids(c))
}
