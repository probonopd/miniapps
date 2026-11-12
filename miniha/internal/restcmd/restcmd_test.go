/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package restcmd

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/probonopd/miniha/internal/core"
)

type seen struct {
	method, path, query, body, contentType, header, user, pass string
}

func server(t *testing.T, status int) (*httptest.Server, func() seen) {
	t.Helper()
	var mu sync.Mutex
	var last seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		u, p, _ := r.BasicAuth()
		mu.Lock()
		last = seen{r.Method, r.URL.Path, r.URL.RawQuery, string(body), r.Header.Get("Content-Type"), r.Header.Get("X-Token"), u, p}
		mu.Unlock()
		w.WriteHeader(status)
		io.WriteString(w, "result body")
	}))
	t.Cleanup(srv.Close)
	return srv, func() seen {
		mu.Lock()
		defer mu.Unlock()
		return last
	}
}

func TestRestCommand(t *testing.T) {
	srv, got := server(t, 200)
	c := core.New()
	c.SetState("sensor.token", "s3cret", nil, core.Context{})
	err := Register(c, map[string]any{
		"notify_phone": map[string]any{
			"url":          srv.URL + "/api/{{ room }}?level={{ level }}",
			"method":       "post",
			"payload":      `{"msg": "{{ message }}", "token": "{{ states('sensor.token') }}"}`,
			"content_type": "application/json",
			"headers":      map[string]any{"X-Token": "t-{{ room }}"},
			"username":     "admin",
			"password":     "pw",
			"timeout":      5,
			"verify_ssl":   false,
		},
		"ping": map[string]any{"url": srv.URL + "/ping", "insecure_cipher": true},
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.CallService(context.Background(), "rest_command", "notify_phone", map[string]any{"room": "kitchen", "level": 3, "message": "hi"}, core.Context{})
	if err != nil {
		t.Fatal(err)
	}
	s := got()
	want := seen{"POST", "/api/kitchen", "level=3", `{"msg": "hi", "token": "s3cret"}`, "application/json", "t-kitchen", "admin", "pw"}
	if s != want {
		t.Fatalf("got %+v\nwant %+v", s, want)
	}
	// The default method is get and no body is sent.
	if _, err := c.CallService(context.Background(), "rest_command", "ping", nil, core.Context{}); err != nil {
		t.Fatal(err)
	}
	if s := got(); s.method != "GET" || s.path != "/ping" || s.body != "" || s.user != "" {
		t.Fatalf("ping %+v", s)
	}
}

func TestRestCommandHTTPError(t *testing.T) {
	srv, _ := server(t, 500)
	c := core.New()
	if err := Register(c, map[string]any{"fail": map[string]any{"url": srv.URL, "method": "delete"}}, slog.Default()); err != nil {
		t.Fatal(err)
	}
	_, err := c.CallService(context.Background(), "rest_command", "fail", nil, core.Context{})
	if err == nil || !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "result body") {
		t.Fatalf("error: %v", err)
	}
}

func TestRestCommandTemplateError(t *testing.T) {
	srv, _ := server(t, 200)
	c := core.New()
	if err := Register(c, map[string]any{"bad": map[string]any{"url": srv.URL + "/{{ 1 / 0 }}"}}, slog.Default()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CallService(context.Background(), "rest_command", "bad", nil, core.Context{}); err == nil {
		t.Fatal("template error ignored")
	}
}

func TestRestCommandConfigErrors(t *testing.T) {
	for name, cfg := range map[string]map[string]any{
		"no url":         {"a": map[string]any{"method": "get"}},
		"bad method":     {"a": map[string]any{"url": "http://x", "method": "trace"}},
		"unknown option": {"a": map[string]any{"url": "http://x", "bogus": 1}},
		"bad name":       {"Not A Slug": map[string]any{"url": "http://x"}},
		"not a mapping":  {"a": "http://x"},
		"bad template":   {"a": map[string]any{"url": "http://x/{{"}},
		"bad timeout":    {"a": map[string]any{"url": "http://x", "timeout": "soon"}},
		"password only":  {"a": map[string]any{"url": "http://x", "password": "p"}},
	} {
		c := core.New()
		if err := Register(c, cfg, slog.Default()); err == nil {
			t.Errorf("%s: accepted", name)
		}
		for key := range cfg {
			if c.HasService("rest_command", key) {
				t.Errorf("%s: registered a service despite the error", name)
			}
		}
	}
}
