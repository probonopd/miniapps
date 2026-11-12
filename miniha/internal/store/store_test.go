/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package store

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/probonopd/miniha/internal/core"
)

func TestConfig(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "configuration.yaml")
	os.WriteFile(p, []byte(DefaultConfig), 0o644)
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP.ServerPort != 8123 || cfg.MQTT.BirthMessage.Payload != "online" || cfg.MQTT.WillMessage.Topic != "homeassistant/status" {
		t.Fatalf("%+v", cfg)
	}
	os.WriteFile(p, []byte("http:\n  server_prot: 1\n"), 0o644)
	if _, err := LoadConfig(p); err == nil {
		t.Fatal("unknown key accepted")
	}
	os.WriteFile(p, []byte("homeassistant:\n  time_zone: Mars/Olympus\n"), 0o644)
	if _, err := LoadConfig(p); err == nil {
		t.Fatal("bad time zone accepted")
	}
}

func TestBrokersConfig(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "configuration.yaml")
	os.WriteFile(p, []byte("mqtt:\n  discovery_prefix: ha\n  brokers:\n    - name: public\n      host: example.org\n      tls: true\n    - name: site2\n      host: 10.0.0.2\n      discovery_prefix: other\n"), 0o644)
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.MQTT.EmbeddedEnabled() {
		t.Fatal("embedded broker must stay on with a brokers list")
	}
	bs := cfg.MQTT.ExternalBrokers()
	if len(bs) != 2 || bs[0].Name != "public" || bs[0].DiscoveryPrefix != "ha" || bs[1].DiscoveryPrefix != "other" {
		t.Fatalf("%+v", bs)
	}
	os.WriteFile(p, []byte("mqtt:\n  external_broker:\n    host: 10.0.0.1\n"), 0o644)
	cfg, err = LoadConfig(p)
	if err != nil || cfg.MQTT.EmbeddedEnabled() || len(cfg.MQTT.ExternalBrokers()) != 1 || cfg.MQTT.ExternalBrokers()[0].Name != "" {
		t.Fatalf("legacy shorthand: %v %+v", err, cfg.MQTT)
	}
	for _, bad := range []string{
		"mqtt:\n  brokers:\n    - name: Bad Name\n      host: x\n",
		"mqtt:\n  brokers:\n    - name: a\n      host: x\n    - name: a\n      host: y\n",
		"mqtt:\n  brokers:\n    - name: a\n",
		"mqtt:\n  embedded_broker: false\n",
	} {
		os.WriteFile(p, []byte(bad), 0o644)
		if _, err := LoadConfig(p); err == nil {
			t.Errorf("accepted: %q", bad)
		}
	}
}

func TestAuth(t *testing.T) {
	p := filepath.Join(t.TempDir(), "auth.json")
	a, err := LoadAuth(p)
	if err != nil {
		t.Fatal(err)
	}
	if a.HasPassword() || a.CheckPassword("x") {
		t.Fatal("empty store must not authenticate")
	}
	if err := a.SetPassword("correct horse"); err != nil {
		t.Fatal(err)
	}
	plain, tok, err := a.CreateToken("test")
	if err != nil {
		t.Fatal(err)
	}
	// Reload from disk: password and token survive, plain token is not stored.
	a2, err := LoadAuth(p)
	if err != nil {
		t.Fatal(err)
	}
	if !a2.CheckPassword("correct horse") || a2.CheckPassword("wrong") {
		t.Fatal("password check wrong after reload")
	}
	if got, ok := a2.ValidateToken(plain); !ok || got.ID != tok.ID {
		t.Fatal("token not valid after reload")
	}
	if _, ok := a2.ValidateToken("miniha_nope"); ok {
		t.Fatal("bogus token accepted")
	}
	raw, _ := os.ReadFile(p)
	if string(raw) == "" || contains(raw, plain) {
		t.Fatal("plain token stored on disk")
	}
	if err := a2.DeleteToken(tok.ID); err != nil || len(a2.ListTokens()) != 0 {
		t.Fatal("delete failed")
	}
}

func contains(raw []byte, s string) bool {
	return len(s) > 0 && len(raw) >= len(s) && string(raw) != "" && indexOf(string(raw), s) >= 0
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}

func TestRegistryRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "registry.json")
	c := core.New()
	s := NewRegistrySaver(p, c, time.Millisecond, slog.Default())
	c.OnRegistryChange(s.Trigger)
	dev, _ := c.RegisterDevice(core.DeviceInfo{Identifiers: [][2]string{{"mqtt", "d1"}}, Name: "Dev", SuggestedArea: "Kitchen"})
	e, _ := c.RegisterEntity(core.EntityRegistration{Domain: "sensor", Platform: "mqtt", UniqueID: "u1", OriginalName: "Temp", HasEntityName: true, DeviceID: dev.ID})
	c.UpdateEntity(e.EntityID, map[string]any{"name": "Renamed"})
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	c2 := core.New()
	s2 := NewRegistrySaver(p, c2, time.Millisecond, slog.Default())
	if err := s2.Load(); err != nil {
		t.Fatal(err)
	}
	again, _ := c2.RegisterEntity(core.EntityRegistration{Domain: "sensor", Platform: "mqtt", UniqueID: "u1", OriginalName: "Temp", HasEntityName: true, DeviceID: dev.ID})
	if again.EntityID != e.EntityID || again.Name != "Renamed" {
		t.Fatalf("registry not restored: %+v", again)
	}
	if d := c2.Device(dev.ID); d == nil || d.AreaID != "kitchen" || len(c2.Areas()) != 1 {
		t.Fatalf("device/area not restored: %+v", d)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestConfigSecrets(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "configuration.yaml")
	writeFile(t, filepath.Join(dir, "secrets.yaml"), "cloud_pw: \"p: #1 \\\"q\\\"\"\nbroker_port: 8883\n")
	writeFile(t, p, "# password: !secret not_looked_up\nmqtt:\n  brokers:\n    - name: cloud\n      host: example.org\n      port: !secret broker_port\n      password: !secret cloud_pw  # trailing !secret ignored\n")
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	b := cfg.MQTT.Brokers[0]
	if b.Password != `p: #1 "q"` || b.Port != 8883 {
		t.Fatalf("%+v", b)
	}

	writeFile(t, p, "mqtt:\n  external_broker:\n    host: x\n    password: !secret absent\n")
	if _, err := LoadConfig(p); err == nil || !strings.Contains(err.Error(), "absent") {
		t.Fatalf("missing key not named: %v", err)
	}
	os.Remove(filepath.Join(dir, "secrets.yaml"))
	if _, err := LoadConfig(p); err == nil || !strings.Contains(err.Error(), "secrets.yaml") {
		t.Fatalf("missing secrets.yaml not reported: %v", err)
	}
}

func TestConfigTLSOptions(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "configuration.yaml")
	writeFile(t, filepath.Join(dir, "a.crt"), "x")
	writeFile(t, filepath.Join(dir, "a.key"), "x")
	writeFile(t, p, "http:\n  ssl_certificate: a.crt\n  ssl_key: a.key\nmqtt:\n  tls_listen: \":8883\"\n  tls_certificate: a.crt\n  tls_key: a.key\n")
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP.SSLCertificate != filepath.Join(dir, "a.crt") || cfg.MQTT.TLSKey != filepath.Join(dir, "a.key") {
		t.Fatalf("paths not resolved: %+v %+v", cfg.HTTP, cfg.MQTT)
	}
	for _, bad := range []string{
		"http:\n  ssl_certificate: a.crt\n",
		"http:\n  ssl_certificate: a.crt\n  ssl_key: missing.key\n",
		"mqtt:\n  tls_listen: \":8883\"\n",
		"mqtt:\n  tls_listen: \":8883\"\n  tls_certificate: a.crt\n  tls_key: missing.key\n",
		"mqtt:\n  tls_certificate: a.crt\n  tls_key: a.key\n",
	} {
		writeFile(t, p, bad)
		if _, err := LoadConfig(p); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestConfigManualEntities(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "configuration.yaml")
	writeFile(t, p, `mqtt:
  switch:
    - name: Pump
      command_topic: pump/set
      qos: 1
  sensor:
    - name: Temp
      state_topic: t/state
      value_template: "{{ value_json.t }}"
      expire_after: 60
      device:
        identifiers: [dev1]
        name: Dev
    - name: Hum
      state_topic: h/state
`)
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.MQTT.ManualEntities()
	if len(got) != 3 || got[0].Component != "sensor" || got[2].Component != "switch" {
		t.Fatalf("%+v", got)
	}
	if got[0].Config["state_topic"] != "t/state" || got[0].Config["expire_after"] == nil {
		t.Fatalf("%+v", got[0].Config)
	}
	if dev, ok := got[0].Config["device"].(map[string]any); !ok || dev["name"] != "Dev" {
		t.Fatalf("nested map lost: %#v", got[0].Config["device"])
	}
}
