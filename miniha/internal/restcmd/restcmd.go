/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

// Package restcmd provides HA's rest_command integration: every entry of
// the rest_command: section becomes a service that sends an HTTP request.
package restcmd

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/probonopd/miniha/internal/automation"
	"github.com/probonopd/miniha/internal/core"
	"github.com/probonopd/miniha/internal/template"
)

const (
	defaultTimeout = 10 * time.Second
	// maxResponse bounds what is read of a response; it only feeds the
	// debug log and error messages.
	maxResponse = 64 << 10
)

var methods = map[string]bool{"get": true, "post": true, "put": true, "patch": true, "delete": true}

type command struct {
	name        string
	method      string
	url         *template.Template
	payload     *template.Template
	headers     map[string]*template.Template
	contentType string
	username    string
	password    string
	timeout     time.Duration
	client      *http.Client
	env         *template.Env
	log         *slog.Logger
}

// Register creates the service rest_command.<name> for each entry of cfg.
// The whole section is validated before anything is registered.
func Register(c *core.Core, cfg map[string]any, log *slog.Logger) error {
	names := make([]string, 0, len(cfg))
	for name := range cfg {
		names = append(names, name)
	}
	sort.Strings(names)
	env := automation.EnvFor(c)
	var cmds []*command
	var errs []error
	for _, name := range names {
		cmd, err := parse(name, cfg[name], env, log)
		if err != nil {
			errs = append(errs, fmt.Errorf("rest_command %s: %w", name, err))
			continue
		}
		cmds = append(cmds, cmd)
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	for _, cmd := range cmds {
		c.RegisterService("rest_command", cmd.name, cmd.call, core.ServiceDesc{
			Name:        cmd.name,
			Description: "Sends an HTTP " + strings.ToUpper(cmd.method) + " request",
		})
	}
	return nil
}

func scalar(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	}
	return fmt.Sprint(v)
}

func parseTemplate(what string, src string) (*template.Template, error) {
	t, err := template.Parse(src)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	return t, nil
}

func parse(name string, raw any, env *template.Env, log *slog.Logger) (*command, error) {
	if core.Slugify(name) != name {
		return nil, fmt.Errorf("name must be a slug such as %q", core.Slugify(name))
	}
	opts, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("options must be a mapping")
	}
	cmd := &command{name: name, method: "get", timeout: defaultTimeout, env: env, log: log, headers: map[string]*template.Template{}}
	verifySSL := true
	var err error
	for key, v := range opts {
		switch key {
		case "url":
			if cmd.url, err = parseTemplate("url", scalar(v)); err != nil {
				return nil, err
			}
		case "method":
			cmd.method = strings.ToLower(scalar(v))
			if !methods[cmd.method] {
				return nil, fmt.Errorf("method %q must be one of get, post, put, patch, delete", scalar(v))
			}
		case "headers":
			hm, ok := v.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("headers must be a mapping")
			}
			for hk, hv := range hm {
				if cmd.headers[hk], err = parseTemplate("header "+hk, scalar(hv)); err != nil {
					return nil, err
				}
			}
		case "payload":
			if cmd.payload, err = parseTemplate("payload", scalar(v)); err != nil {
				return nil, err
			}
		case "content_type":
			cmd.contentType = scalar(v)
		case "username":
			cmd.username = scalar(v)
		case "password":
			cmd.password = scalar(v)
		case "timeout":
			secs, perr := strconv.ParseFloat(scalar(v), 64)
			if perr != nil || secs <= 0 {
				return nil, fmt.Errorf("timeout must be a positive number of seconds")
			}
			cmd.timeout = time.Duration(secs * float64(time.Second))
		case "verify_ssl":
			b, ok := v.(bool)
			if !ok {
				return nil, fmt.Errorf("verify_ssl must be true or false")
			}
			verifySSL = b
		case "insecure_cipher":
			// Accepted for compatibility; Go has no equivalent knob.
		default:
			return nil, fmt.Errorf("unknown option %q", key)
		}
	}
	if cmd.url == nil || strings.TrimSpace(cmd.url.Source()) == "" {
		return nil, fmt.Errorf("url is required")
	}
	if cmd.password != "" && cmd.username == "" {
		return nil, fmt.Errorf("password needs username")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if !verifySSL {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	cmd.client = &http.Client{Transport: transport}
	return cmd, nil
}

func (cmd *command) render(t *template.Template, what string, vars map[string]any) (string, error) {
	out, err := t.Render(vars, cmd.env)
	if err != nil {
		return "", fmt.Errorf("rest_command.%s %s: %w", cmd.name, what, err)
	}
	return out, nil
}

func (cmd *command) call(ctx context.Context, call core.ServiceCall) error {
	vars := call.Data
	url, err := cmd.render(cmd.url, "url", vars)
	if err != nil {
		return err
	}
	var body io.Reader
	if cmd.payload != nil {
		payload, err := cmd.render(cmd.payload, "payload", vars)
		if err != nil {
			return err
		}
		body = strings.NewReader(payload)
	}
	ctx, cancel := context.WithTimeout(ctx, cmd.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, strings.ToUpper(cmd.method), strings.TrimSpace(url), body)
	if err != nil {
		return fmt.Errorf("rest_command.%s: %w", cmd.name, err)
	}
	for k, t := range cmd.headers {
		v, err := cmd.render(t, "header "+k, vars)
		if err != nil {
			return err
		}
		req.Header.Set(k, v)
	}
	if cmd.contentType != "" {
		req.Header.Set("Content-Type", cmd.contentType)
	}
	if cmd.username != "" {
		req.SetBasicAuth(cmd.username, cmd.password)
	}
	resp, err := cmd.client.Do(req)
	if err != nil {
		return fmt.Errorf("rest_command.%s: %w", cmd.name, err)
	}
	defer resp.Body.Close()
	text, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return fmt.Errorf("rest_command.%s: reading response: %w", cmd.name, err)
	}
	cmd.log.Debug("rest_command response", "service", cmd.name, "url", req.URL.Redacted(), "status", resp.StatusCode, "body", string(text))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("rest_command.%s: %s %s returned %s: %s", cmd.name, req.Method, req.URL.Redacted(), resp.Status, snippet(text))
	}
	return nil
}

// snippet shortens a response body for an error message.
func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}
