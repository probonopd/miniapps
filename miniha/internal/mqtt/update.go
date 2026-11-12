/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package mqtt

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/probonopd/miniha/internal/core"
	"github.com/probonopd/miniha/internal/template"
)

func init() {
	components["update"] = newUpdate
}

// UpdateEntityFeature bits, as in HA.
const (
	updateFeatInstall  = 1
	updateFeatProgress = 4
)

type updateEntity struct {
	e          *entity
	cmdTopic   string
	payload    string
	precision  int
	installed  string
	latest     string
	title      string
	summary    string
	releaseURL string
	picture    string
	inProgress bool
	percentage *float64
	progress   bool // update_percentage has been reported at least once
}

func newUpdate(e *entity) (component, error) {
	cfg := e.cfg
	if !cfg.Has("state_topic") && !cfg.Has("latest_version_topic") {
		return nil, fmt.Errorf("state_topic or latest_version_topic is required")
	}
	if cfg.Has("command_topic") != cfg.Has("payload_install") {
		return nil, fmt.Errorf("command_topic and payload_install must be set together")
	}
	u := &updateEntity{
		e: e, cmdTopic: cfg.Str("command_topic"), payload: cfg.Str("payload_install"), precision: cfg.Int("display_precision", 0),
		title: cfg.Str("title"), summary: cfg.Str("release_summary"), releaseURL: cfg.Str("release_url"), picture: cfg.Str("entity_picture"),
	}
	if len([]rune(u.summary)) > 255 {
		return nil, fmt.Errorf("release_summary is longer than 255 characters")
	}
	stateTpl, err := cfg.Template("value_template")
	if err != nil {
		return nil, err
	}
	latestTpl, err := cfg.Template("latest_version_template")
	if err != nil {
		return nil, err
	}
	e.attrs = u.attributes
	if cfg.Has("state_topic") {
		e.subscribe(cfg.Str("state_topic"), func(m Message) { u.onState(stateTpl, m) })
	}
	if cfg.Has("latest_version_topic") {
		e.subscribe(cfg.Str("latest_version_topic"), func(m Message) { u.onLatest(latestTpl, m) })
	}
	if u.cmdTopic != "" {
		e.d.core.RegisterController(e.entityID, u)
	}
	u.refresh()
	return u, nil
}

func (u *updateEntity) stop() {}

func (u *updateEntity) rendered(tpl *template.Template, m Message) (string, bool) {
	if tpl == nil {
		return string(m.Payload), true
	}
	s, err := u.e.render(tpl, m.Payload)
	if err != nil {
		u.e.d.log.Warn("update template failed", "entity_id", u.e.entityID, "err", err)
		return "", false
	}
	return s, true
}

func (u *updateEntity) onLatest(tpl *template.Template, m Message) {
	payload, ok := u.rendered(tpl, m)
	if !ok || payload == "" {
		return
	}
	u.e.mu.Lock()
	u.latest = payload
	u.e.mu.Unlock()
	u.refresh()
}

func (u *updateEntity) onState(tpl *template.Template, m Message) {
	payload, ok := u.rendered(tpl, m)
	if !ok || payload == "" {
		return
	}
	var obj map[string]any
	// A bare version such as "2024.5" is valid JSON but not an object, and
	// is then the installed version itself.
	if err := json.Unmarshal([]byte(payload), &obj); err != nil || obj == nil {
		u.e.mu.Lock()
		u.installed = payload
		u.e.mu.Unlock()
		u.refresh()
		return
	}
	u.e.mu.Lock()
	str := func(key string, dst *string) {
		if v, ok := obj[key]; ok {
			*dst = coerce(v)
		}
	}
	str("installed_version", &u.installed)
	str("latest_version", &u.latest)
	str("title", &u.title)
	str("release_summary", &u.summary)
	str("release_url", &u.releaseURL)
	str("entity_picture", &u.picture)
	if v, ok := obj["in_progress"].(bool); ok {
		u.inProgress = v
	}
	if v, present := obj["update_percentage"]; present {
		u.progress = true
		if f, ok := num(v); ok {
			u.percentage = &f
		} else {
			u.percentage = nil
		}
	}
	u.e.mu.Unlock()
	u.refresh()
}

// refresh derives the on/off state from the two versions and writes it.
func (u *updateEntity) refresh() {
	u.e.mu.Lock()
	switch {
	case u.installed == "" || u.latest == "":
		u.e.state = ""
	case u.installed != u.latest:
		u.e.state = core.StateOn
	default:
		u.e.state = core.StateOff
	}
	u.e.mu.Unlock()
	u.e.write(false)
}

// attributes runs under the entity lock, called from entity.write.
func (u *updateEntity) attributes() map[string]any {
	features := 0
	if u.cmdTopic != "" {
		features |= updateFeatInstall
	}
	if u.progress {
		features |= updateFeatProgress
	}
	a := map[string]any{
		"auto_update": false, "display_precision": u.precision, "skipped_version": nil, "supported_features": features,
		"installed_version": nil, "latest_version": nil, "title": nil, "release_summary": nil, "release_url": nil,
		"in_progress": u.inProgress, "update_percentage": nil,
	}
	for k, v := range map[string]string{"installed_version": u.installed, "latest_version": u.latest, "title": u.title, "release_summary": u.summary, "release_url": u.releaseURL} {
		if v != "" {
			a[k] = v
		}
	}
	if u.picture != "" {
		a["entity_picture"] = u.picture
	}
	if u.progress && u.inProgress && u.percentage != nil {
		a["update_percentage"] = *u.percentage
	}
	if v := u.e.cfg.Str("device_class"); v != "" {
		a["device_class"] = v
	}
	return a
}

func (u *updateEntity) Install(_ context.Context, version string, backup bool) error {
	u.e.mu.Lock()
	latest := u.latest
	u.e.mu.Unlock()
	if version != "" && version != latest {
		return fmt.Errorf("only the latest version %q can be installed", latest)
	}
	if backup {
		return fmt.Errorf("update does not support backups")
	}
	return u.e.publish(u.cmdTopic, u.payload)
}
