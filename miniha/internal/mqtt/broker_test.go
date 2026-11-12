/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package mqtt

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func freePort(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func TestBrokerInlineRoundTrip(t *testing.T) {
	b, err := NewBroker(BrokerConfig{Listen: freePort(t)}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Start(); err != nil {
		t.Fatal(err)
	}
	defer b.Stop()
	got := make(chan Message, 4)
	unsub1, _ := b.Subscribe("a/+", func(m Message) { got <- m })
	unsub2, _ := b.Subscribe("a/+", func(m Message) { got <- m })
	if err := b.Publish("a/b", []byte("1"), 0, false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		select {
		case m := <-got:
			if m.Topic != "a/b" || string(m.Payload) != "1" {
				t.Fatalf("got %+v", m)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("message not delivered to both callbacks")
		}
	}
	unsub1()
	b.Publish("a/c", []byte("2"), 0, false)
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("second callback lost after first unsubscribed")
	}
	unsub2()
	b.Publish("a/d", []byte("3"), 0, false)
	select {
	case m := <-got:
		t.Fatalf("delivered after unsubscribe: %+v", m)
	case <-time.After(200 * time.Millisecond):
	}
	// Retained messages reach a later inline subscriber.
	b.Publish("r/1", []byte("kept"), 0, true)
	retained := make(chan Message, 1)
	unsub3, _ := b.Subscribe("r/#", func(m Message) { retained <- m })
	defer unsub3()
	select {
	case m := <-retained:
		if string(m.Payload) != "kept" || !m.Retained {
			t.Fatalf("got %+v", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("retained message not replayed to inline subscriber")
	}
}

func TestBrokerRetainedPersistence(t *testing.T) {
	dir := t.TempDir()
	b, err := NewBroker(BrokerConfig{Listen: freePort(t), DataDir: dir}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Start(); err != nil {
		t.Fatal(err)
	}
	b.Publish("homeassistant/sensor/x/config", []byte(`{"name":"x"}`), 0, true)
	b.Publish("gone/1", []byte("v"), 0, true)
	b.Publish("gone/1", nil, 0, true) // cleared again, must not come back
	b.Publish("plain/1", []byte("n"), 0, false)
	// Stop flushes immediately, well before the debounce delay.
	if err := b.Stop(); err != nil {
		t.Fatal(err)
	}

	b2, err := NewBroker(BrokerConfig{Listen: freePort(t), DataDir: dir}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if err := b2.Start(); err != nil {
		t.Fatal(err)
	}
	defer b2.Stop()
	got := make(chan Message, 8)
	unsub, err := b2.Subscribe("#", func(m Message) { got <- m })
	if err != nil {
		t.Fatal(err)
	}
	defer unsub()
	select {
	case m := <-got:
		if m.Topic != "homeassistant/sensor/x/config" || string(m.Payload) != `{"name":"x"}` || !m.Retained {
			t.Fatalf("got %+v", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("retained message lost across restart")
	}
	select {
	case m := <-got:
		t.Fatalf("unexpected message after restart: %+v", m)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestBrokerTLSOptionsValidated(t *testing.T) {
	if _, err := NewBroker(BrokerConfig{Listen: freePort(t), TLSListen: freePort(t)}, slog.Default()); err == nil {
		t.Fatal("TLS listener without certificate accepted")
	}
	if _, err := NewBroker(BrokerConfig{Listen: freePort(t), TLSListen: freePort(t), TLSCertificate: "/nonexistent.crt", TLSKey: "/nonexistent.key"}, slog.Default()); err == nil {
		t.Fatal("missing certificate files accepted")
	}
}

func writeTestCert(t *testing.T, dir string) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "miniha-test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath = filepath.Join(dir, "c.crt"), filepath.Join(dir, "c.key")
	os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600)
	return certPath, keyPath
}

func TestBrokerTLSListener(t *testing.T) {
	cert, key := writeTestCert(t, t.TempDir())
	addr := freePort(t)
	b, err := NewBroker(BrokerConfig{Listen: freePort(t), TLSListen: addr, TLSCertificate: cert, TLSKey: key}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Start(); err != nil {
		t.Fatal(err)
	}
	defer b.Stop()
	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
}
