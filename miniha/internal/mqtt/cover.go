/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package mqtt

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/probonopd/miniha/internal/template"
)

func init() {
	components["cover"] = newCover
}

// CoverEntityFeature bits, as in HA.
const (
	coverFeatOpen         = 1
	coverFeatClose        = 2
	coverFeatSetPosition  = 4
	coverFeatStop         = 8
	coverFeatOpenTilt     = 16
	coverFeatCloseTilt    = 32
	coverFeatStopTilt     = 64
	coverFeatSetTiltPos   = 128
	coverStateOpen        = "open"
	coverStateOpening     = "opening"
	coverStateClosed      = "closed"
	coverStateClosing     = "closing"
	coverStatePayloadNone = "None"
)

// devRange maps between HA's 0..100 percentages and a device's own range.
// closed is the device value for 0 percent, open the one for 100 percent;
// open < closed is a reversed device and works unchanged.
type devRange struct {
	closed, open int
}

func (r devRange) percent(v float64) int {
	p := math.RoundToEven((v - float64(r.closed)) / float64(r.open-r.closed) * 100)
	return int(max(0, min(100, p)))
}

func (r devRange) device(percent int) int {
	return int(math.RoundToEven(float64(r.closed) + float64(percent)/100*float64(r.open-r.closed)))
}

func (r devRange) contains(v float64) bool {
	lo, hi := float64(min(r.closed, r.open)), float64(max(r.closed, r.open))
	return v >= lo && v <= hi
}

type cover struct {
	e *entity

	cmdTopic, posTopic, setPosTopic string
	tiltCmdTopic, tiltStatusTopic   string

	payloadOpen, payloadClose, payloadStop, payloadStopTilt string
	hasOpen, hasClose, hasStop, hasStopTilt                 bool
	stateOpen, stateOpening, stateClosed, stateClosing      string
	stateStopped                                            string

	pos, tilt         devRange
	tiltOpened        int
	tiltClosed        int
	setPosTpl         *template.Template
	tiltCmdTpl        *template.Template
	optimistic        bool
	tiltOptimistic    bool
	hasStateTopic     bool
	features          int
	position, tiltPos *int // percent, nil while unknown
}

// disableable reads a payload option where an empty string or null turns
// the command off, as HA documents for payload_close/stop.
func disableable(cfg Config, key, def string) (string, bool) {
	v, ok := cfg[key]
	if !ok {
		return def, true
	}
	s := coerce(v)
	return s, v != nil && s != ""
}

func newCover(e *entity) (component, error) {
	cfg := e.cfg
	c := &cover{e: e}
	c.cmdTopic, c.posTopic, c.setPosTopic = cfg.Str("command_topic"), cfg.Str("position_topic"), cfg.Str("set_position_topic")
	c.tiltCmdTopic, c.tiltStatusTopic = cfg.Str("tilt_command_topic"), cfg.Str("tilt_status_topic")
	if c.cmdTopic == "" && c.setPosTopic == "" && c.tiltCmdTopic == "" {
		return nil, fmt.Errorf("one of command_topic, set_position_topic or tilt_command_topic is required")
	}
	if c.setPosTopic != "" && c.posTopic == "" {
		return nil, fmt.Errorf("set_position_topic must be set together with position_topic")
	}
	c.hasStateTopic = cfg.Has("state_topic")
	c.payloadOpen, c.hasOpen = disableable(cfg, "payload_open", "OPEN")
	c.payloadClose, c.hasClose = disableable(cfg, "payload_close", "CLOSE")
	c.payloadStop, c.hasStop = disableable(cfg, "payload_stop", "STOP")
	c.payloadStopTilt, c.hasStopTilt = disableable(cfg, "payload_stop_tilt", "stop")
	c.stateOpen, c.stateOpening = cfg.StrDef("state_open", coverStateOpen), cfg.StrDef("state_opening", coverStateOpening)
	c.stateClosed, c.stateClosing = cfg.StrDef("state_closed", coverStateClosed), cfg.StrDef("state_closing", coverStateClosing)
	c.stateStopped = cfg.StrDef("state_stopped", "stopped")
	c.pos = devRange{closed: cfg.Int("position_closed", 0), open: cfg.Int("position_open", 100)}
	c.tilt = devRange{closed: cfg.Int("tilt_min", 0), open: cfg.Int("tilt_max", 100)}
	if c.pos.closed == c.pos.open {
		return nil, fmt.Errorf("position_open and position_closed must differ")
	}
	if c.tilt.closed == c.tilt.open {
		return nil, fmt.Errorf("tilt_min and tilt_max must differ")
	}
	c.tiltClosed, c.tiltOpened = cfg.Int("tilt_closed_value", 0), cfg.Int("tilt_opened_value", 100)
	c.optimistic = cfg.Bool("optimistic", !c.hasStateTopic && c.posTopic == "")
	c.tiltOptimistic = cfg.Bool("tilt_optimistic", c.tiltStatusTopic == "")

	var err error
	if c.setPosTpl, err = cfg.Template("set_position_template"); err != nil {
		return nil, err
	}
	if c.tiltCmdTpl, err = cfg.Template("tilt_command_template"); err != nil {
		return nil, err
	}
	posTpl, err := cfg.Template("position_template")
	if err != nil {
		return nil, err
	}
	tiltTpl, err := cfg.Template("tilt_status_template")
	if err != nil {
		return nil, err
	}
	stateTpl, err := cfg.Template("value_template")
	if err != nil {
		return nil, err
	}

	if c.cmdTopic != "" {
		if c.hasOpen {
			c.features |= coverFeatOpen
		}
		if c.hasClose {
			c.features |= coverFeatClose
		}
		if c.hasStop {
			c.features |= coverFeatStop
		}
	}
	if c.setPosTopic != "" {
		c.features |= coverFeatSetPosition
	}
	if c.tiltCmdTopic != "" {
		c.features |= coverFeatOpenTilt | coverFeatCloseTilt | coverFeatSetTiltPos
		if c.hasStopTilt {
			c.features |= coverFeatStopTilt
		}
	}

	e.attrs = c.attributes
	if c.hasStateTopic {
		e.subscribe(cfg.Str("state_topic"), func(m Message) { c.onState(stateTpl, m) })
	}
	if c.posTopic != "" {
		e.subscribe(c.posTopic, func(m Message) { c.onPosition(posTpl, m) })
	}
	if c.tiltStatusTopic != "" {
		e.subscribe(c.tiltStatusTopic, func(m Message) { c.onTilt(tiltTpl, m) })
	}
	e.d.core.RegisterController(e.entityID, c)
	e.write(false)
	return c, nil
}

func (c *cover) stop() {}

// attributes runs under the entity lock, called from entity.write.
func (c *cover) attributes() map[string]any {
	a := map[string]any{"supported_features": c.features}
	if v := c.e.cfg.Str("device_class"); v != "" {
		a["device_class"] = v
	}
	if c.optimistic {
		a["assumed_state"] = true
	}
	if c.position != nil {
		a["current_position"] = *c.position
	}
	if c.tiltPos != nil {
		a["current_tilt_position"] = *c.tiltPos
	}
	return a
}

// tplVars are the variables HA gives every cover template.
func (c *cover) tplVars(extra map[string]any) map[string]any {
	v := map[string]any{
		"entity_id": c.e.entityID, "name": c.e.d.core.FriendlyName(c.e.entityID),
		"position_open": c.pos.open, "position_closed": c.pos.closed,
		"tilt_min": c.tilt.closed, "tilt_max": c.tilt.open,
	}
	for k, x := range extra {
		v[k] = x
	}
	return v
}

func (c *cover) renderPayload(tpl *template.Template, m Message) (string, bool) {
	payload := string(m.Payload)
	if tpl != nil {
		var err error
		payload, err = tpl.RenderWithPossibleJSON(payload, m.Payload, c.tplVars(nil))
		if err != nil {
			c.e.d.log.Warn("cover template failed", "entity_id", c.e.entityID, "err", err)
			return "", false
		}
	}
	return strings.TrimSpace(payload), true
}

func (c *cover) onState(tpl *template.Template, m Message) {
	var payload string
	if tpl != nil {
		var ok bool
		if payload, ok = c.renderPayload(tpl, m); !ok {
			return
		}
	} else {
		payload = string(m.Payload)
	}
	if payload == "" {
		return
	}
	e := c.e
	e.mu.Lock()
	switch payload {
	case coverStatePayloadNone:
		e.state = ""
	case c.stateOpen:
		e.state = coverStateOpen
	case c.stateOpening:
		e.state = coverStateOpening
	case c.stateClosed:
		e.state = coverStateClosed
	case c.stateClosing:
		e.state = coverStateClosing
	case c.stateStopped:
		// A stopped cover reports neither direction, so the position (or
		// the direction it was travelling) decides open versus closed.
		closed := e.state == coverStateClosing
		if c.posTopic != "" {
			closed = c.position != nil && *c.position == 0
		}
		if closed {
			e.state = coverStateClosed
		} else {
			e.state = coverStateOpen
		}
	default:
		e.mu.Unlock()
		e.d.log.Warn("cover state payload is not supported", "entity_id", e.entityID, "payload", payload)
		return
	}
	e.mu.Unlock()
	e.write(false)
}

func (c *cover) parseRanged(payload string, r devRange, what string) (int, bool) {
	if payload == "" {
		c.e.d.log.Warn("ignoring empty "+what+" message", "entity_id", c.e.entityID)
		return 0, false
	}
	v, err := strconv.ParseFloat(payload, 64)
	if err != nil {
		c.e.d.log.Warn("cover "+what+" payload is not numeric", "entity_id", c.e.entityID, "payload", payload)
		return 0, false
	}
	if !r.contains(v) {
		c.e.d.log.Warn("cover "+what+" payload is out of range", "entity_id", c.e.entityID, "payload", payload)
		return 0, false
	}
	return r.percent(v), true
}

func (c *cover) onPosition(tpl *template.Template, m Message) {
	payload, ok := c.renderPayload(tpl, m)
	if !ok {
		return
	}
	p, ok := c.parseRanged(payload, c.pos, "position")
	if !ok {
		return
	}
	c.e.mu.Lock()
	c.position = &p
	if !c.hasStateTopic {
		// Without a state topic the position is the only truth: anything
		// but fully closed counts as open.
		c.e.state = coverStateOpen
		if p == 0 {
			c.e.state = coverStateClosed
		}
	}
	c.e.mu.Unlock()
	c.e.write(false)
}

func (c *cover) onTilt(tpl *template.Template, m Message) {
	payload, ok := c.renderPayload(tpl, m)
	if !ok {
		return
	}
	p, ok := c.parseRanged(payload, c.tilt, "tilt")
	if !ok {
		return
	}
	c.e.mu.Lock()
	c.tiltPos = &p
	c.e.mu.Unlock()
	c.e.write(false)
}

// CoverState lets cover.toggle choose between opening and closing.
func (c *cover) CoverState() string {
	c.e.mu.Lock()
	defer c.e.mu.Unlock()
	return c.e.state
}

func (c *cover) send(topic, payload string) error {
	return c.e.publish(topic, payload)
}

func (c *cover) needCommand(ok bool, what string) error {
	if !ok || c.cmdTopic == "" {
		return fmt.Errorf("cover does not support %s", what)
	}
	return nil
}

func (c *cover) optimisticState(ctx context.Context, state string, position int) {
	c.e.mu.Lock()
	c.e.state = state
	if c.posTopic != "" {
		c.position = &position
	}
	c.e.mu.Unlock()
	c.e.write(false, ctx)
}

func (c *cover) OpenCover(ctx context.Context) error {
	if err := c.needCommand(c.hasOpen, "open"); err != nil {
		return err
	}
	if err := c.send(c.cmdTopic, c.payloadOpen); err != nil {
		return err
	}
	if c.optimistic {
		c.optimisticState(ctx, coverStateOpen, 100)
	}
	return nil
}

func (c *cover) CloseCover(ctx context.Context) error {
	if err := c.needCommand(c.hasClose, "close"); err != nil {
		return err
	}
	if err := c.send(c.cmdTopic, c.payloadClose); err != nil {
		return err
	}
	if c.optimistic {
		c.optimisticState(ctx, coverStateClosed, 0)
	}
	return nil
}

func (c *cover) StopCover(_ context.Context) error {
	if err := c.needCommand(c.hasStop, "stop"); err != nil {
		return err
	}
	return c.send(c.cmdTopic, c.payloadStop)
}

// rangedPayload renders a position or tilt command: the device value goes
// in as "value", the percentage as the named variable, as HA does.
func (c *cover) rangedPayload(tpl *template.Template, name string, device, percent int) (string, error) {
	if tpl == nil {
		return strconv.Itoa(device), nil
	}
	return tpl.Render(c.tplVars(map[string]any{"value": device, name: percent}), nil)
}

func (c *cover) SetCoverPosition(ctx context.Context, position int) error {
	if c.setPosTopic == "" {
		return fmt.Errorf("cover does not support set_position")
	}
	if position < 0 || position > 100 {
		return fmt.Errorf("position must be 0..100")
	}
	payload, err := c.rangedPayload(c.setPosTpl, "position", c.pos.device(position), position)
	if err != nil {
		return err
	}
	if err := c.send(c.setPosTopic, payload); err != nil {
		return err
	}
	if c.optimistic {
		state := coverStateOpen
		if position == 0 {
			state = coverStateClosed
		}
		c.e.mu.Lock()
		c.e.state = state
		c.position = &position
		c.e.mu.Unlock()
		c.e.write(false, ctx)
	}
	return nil
}

func (c *cover) sendTilt(ctx context.Context, device int) error {
	if c.tiltCmdTopic == "" {
		return fmt.Errorf("cover does not support tilt")
	}
	percent := c.tilt.percent(float64(device))
	payload, err := c.rangedPayload(c.tiltCmdTpl, "tilt_position", device, percent)
	if err != nil {
		return err
	}
	if err := c.send(c.tiltCmdTopic, payload); err != nil {
		return err
	}
	if c.tiltOptimistic {
		c.e.mu.Lock()
		c.tiltPos = &percent
		c.e.mu.Unlock()
		c.e.write(false, ctx)
	}
	return nil
}

func (c *cover) OpenCoverTilt(ctx context.Context) error  { return c.sendTilt(ctx, c.tiltOpened) }
func (c *cover) CloseCoverTilt(ctx context.Context) error { return c.sendTilt(ctx, c.tiltClosed) }

func (c *cover) StopCoverTilt(_ context.Context) error {
	if c.tiltCmdTopic == "" || !c.hasStopTilt {
		return fmt.Errorf("cover does not support stop_tilt")
	}
	return c.send(c.tiltCmdTopic, c.payloadStopTilt)
}

func (c *cover) SetCoverTiltPosition(ctx context.Context, position int) error {
	if position < 0 || position > 100 {
		return fmt.Errorf("tilt position must be 0..100")
	}
	return c.sendTilt(ctx, c.tilt.device(position))
}
