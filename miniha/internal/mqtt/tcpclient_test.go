/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package mqtt

import (
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/probonopd/miniha/internal/core"
)

// A minimal MQTT 3.1.1 publisher, enough to prove the TCP path end to
// end: a real client's discovery payload must produce an entity.
func mqttPublish(t *testing.T, addr, topic, payload string, retain bool) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	str := func(s string) []byte { return append([]byte{byte(len(s) >> 8), byte(len(s))}, s...) }
	enc := func(typ byte, body []byte) []byte {
		out := []byte{typ}
		n := len(body)
		for {
			b := byte(n % 128)
			n /= 128
			if n > 0 {
				b |= 0x80
			}
			out = append(out, b)
			if n == 0 {
				break
			}
		}
		return append(out, body...)
	}
	connect := append(str("MQTT"), 4, 0x02, 0, 60)
	connect = append(connect, str("test-client")...)
	conn.Write(enc(0x10, connect))
	ack := make([]byte, 4)
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Read(ack); err != nil || ack[0] != 0x20 || ack[3] != 0 {
		t.Fatalf("connack: %v %v", ack, err)
	}
	flags := byte(0x30)
	if retain {
		flags |= 1
	}
	conn.Write(enc(flags, append(str(topic), payload...)))
	conn.Write(enc(0xe0, nil))
	time.Sleep(50 * time.Millisecond)
}

func TestRealClientDiscovery(t *testing.T) {
	addr := freePort(t)
	b, err := NewBroker(BrokerConfig{Listen: addr}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Start(); err != nil {
		t.Fatal(err)
	}
	defer b.Stop()
	c := core.New()
	d := NewDiscovery(c, b, Options{Prefix: "homeassistant"}, slog.Default())
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	defer d.Stop()
	mqttPublish(t, addr, "homeassistant/sensor/t1/config", `{"name":"Temp","stat_t":"t1/state","uniq_id":"t1","unit_of_meas":"°C"}`, true)
	mqttPublish(t, addr, "t1/state", "23.4", false)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s := c.GetState("sensor.temp"); s != nil && s.State == "23.4" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("entity not created from a real client: %v", ids(c))
}
