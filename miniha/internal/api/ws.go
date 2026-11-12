/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/probonopd/miniha/internal/automation"
	"github.com/probonopd/miniha/internal/core"
	"github.com/probonopd/miniha/internal/template"
)

const (
	// wsMaxMessage bounds one incoming frame; HA's own limit is 4 MB, our
	// commands are small.
	wsMaxMessage = 1 << 20
	// wsAuthTimeout is how long a client may take to authenticate (HA: 10 s).
	wsAuthTimeout  = 10 * time.Second
	wsWriteTimeout = 10 * time.Second
	// wsQueueSize is the per-connection outbound buffer; a client that falls
	// further behind loses events rather than stalling the event bus.
	wsQueueSize = 256
)

// wsError is a command failure with the HA error code the client sees.
type wsError struct{ code, message string }

func (e *wsError) Error() string { return e.message }

func wsErrf(code, format string, args ...any) *wsError {
	return &wsError{code, fmt.Sprintf(format, args...)}
}

// wsMsg is one decoded command. A handler that must send events after its
// result sets after; the reader runs it once the result is queued so the
// client always sees the result first.
type wsMsg struct {
	ID    int
	Type  string
	P     map[string]any
	after func()
}

func (m *wsMsg) str(key string) (string, error) {
	v, present := m.P[key]
	if !present {
		return "", wsErrf("invalid_format", "%s is required", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", wsErrf("invalid_format", "%s must be a string", key)
	}
	return s, nil
}

// optStr returns "" for an absent or null key.
func (m *wsMsg) optStr(key string) (string, error) {
	if v, present := m.P[key]; !present || v == nil {
		return "", nil
	}
	return m.str(key)
}

// pick returns the listed keys that the client sent, so an update only
// touches what was asked for. Values must be strings or null (null clears).
func (m *wsMsg) pick(keys ...string) (map[string]any, error) {
	out := map[string]any{}
	for _, k := range keys {
		v, present := m.P[k]
		if !present {
			continue
		}
		if v != nil {
			if _, ok := v.(string); !ok {
				return nil, wsErrf("invalid_format", "%s must be a string or null", k)
			}
		}
		out[k] = v
	}
	return out, nil
}

func (m *wsMsg) strList(key string) ([]string, error) {
	v, present := m.P[key]
	if !present || v == nil {
		return nil, nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil, wsErrf("invalid_format", "%s must be a list", key)
	}
	out := make([]string, 0, len(list))
	for _, e := range list {
		s, ok := e.(string)
		if !ok {
			return nil, wsErrf("invalid_format", "%s must contain strings", key)
		}
		out = append(out, s)
	}
	return out, nil
}

func (m *wsMsg) obj(key string) (map[string]any, error) {
	v, present := m.P[key]
	if !present || v == nil {
		return nil, nil
	}
	o, ok := v.(map[string]any)
	if !ok {
		return nil, wsErrf("invalid_format", "%s must be an object", key)
	}
	return o, nil
}

type wsPong struct{}

type wsCommand struct {
	run func(c *wsConn, m *wsMsg) (any, error)
	// async commands may take long (a service call talks to hardware) and
	// must not hold up pings and other commands on the same connection.
	async bool
}

var wsCommands = map[string]wsCommand{
	"ping":                          {run: (*wsConn).cmdPing},
	"get_states":                    {run: (*wsConn).cmdGetStates},
	"get_config":                    {run: (*wsConn).cmdGetConfig},
	"get_services":                  {run: (*wsConn).cmdGetServices},
	"subscribe_events":              {run: (*wsConn).cmdSubscribeEvents},
	"unsubscribe_events":            {run: (*wsConn).cmdUnsubscribe},
	"subscribe_entities":            {run: (*wsConn).cmdSubscribeEntities},
	"call_service":                  {run: (*wsConn).cmdCallService, async: true},
	"config/entity_registry/list":   {run: (*wsConn).cmdEntityList},
	"config/entity_registry/get":    {run: (*wsConn).cmdEntityGet},
	"config/entity_registry/update": {run: (*wsConn).cmdEntityUpdate},
	"config/device_registry/list":   {run: (*wsConn).cmdDeviceList},
	"config/device_registry/update": {run: (*wsConn).cmdDeviceUpdate},
	"config/area_registry/list":     {run: (*wsConn).cmdAreaList},
	"config/area_registry/create":   {run: (*wsConn).cmdAreaCreate},
	"config/area_registry/update":   {run: (*wsConn).cmdAreaUpdate},
	"config/area_registry/delete":   {run: (*wsConn).cmdAreaDelete},
	"render_template":               {run: (*wsConn).cmdRenderTemplate},
	"subscribe_trigger":             {run: (*wsConn).cmdSubscribeTrigger},
	"validate_config":               {run: (*wsConn).cmdValidateConfig},
	"supported_features":            {run: (*wsConn).cmdSupportedFeatures},
	"auth/current_user":             {run: (*wsConn).cmdCurrentUser},

	"recorder/statistics_during_period": {run: (*wsConn).cmdStatisticsDuringPeriod},
	"recorder/get_statistics_metadata":  {run: (*wsConn).cmdStatisticsMetadata},
	"recorder/info":                     {run: (*wsConn).cmdRecorderInfo},
	"history/history_during_period":     {run: (*wsConn).cmdHistoryDuringPeriod},
	"logbook/get_events":                {run: (*wsConn).cmdLogbookEvents},
	"persistent_notification/get":       {run: (*wsConn).cmdNotificationsGet},
	"persistent_notification/subscribe": {run: (*wsConn).cmdNotificationsSubscribe},
	"lovelace/config":                   {run: (*wsConn).cmdLovelaceConfig},
	"lovelace/resources":                {run: (*wsConn).cmdLovelaceResources},
	"frontend/get_themes":               {run: (*wsConn).cmdGetThemes},
	"config/core/check_config":          {run: (*wsConn).cmdCheckConfig},
	"system_health/info":                {run: (*wsConn).cmdSystemHealth},
}

// wsConn is one authenticated client. The reader goroutine (the HTTP
// handler) dispatches commands; the writer goroutine owns all writes so
// that core event handlers never block on a slow client.
type wsConn struct {
	s      *Server
	ctx    context.Context
	cancel context.CancelFunc
	out    chan any
	wg     sync.WaitGroup

	lastID int // reader goroutine only

	mu   sync.Mutex
	subs map[int]func()
}

func (s *Server) websocket(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		// Accept already answered the HTTP request with the reason.
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(wsMaxMessage)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	if !s.wsAuthenticate(ctx, conn, r) {
		return
	}

	c := &wsConn{s: s, ctx: ctx, cancel: cancel, out: make(chan any, wsQueueSize), subs: map[int]func(){}}
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		c.writeLoop(conn)
	}()
	defer func() {
		cancel()
		c.closeSubs()
		c.wg.Wait()
		<-writerDone
	}()

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if !c.handle(data) {
			conn.Close(websocket.StatusUnsupportedData, "message is not a JSON object")
			return
		}
	}
}

// wsAuthenticate runs the auth phase. It is written directly because the
// writer goroutine does not exist yet.
func (s *Server) wsAuthenticate(ctx context.Context, conn *websocket.Conn, r *http.Request) bool {
	write := func(v any) error {
		wctx, cancel := context.WithTimeout(ctx, wsWriteTimeout)
		defer cancel()
		b, err := wsMarshal(v)
		if err != nil {
			return err
		}
		return conn.Write(wctx, websocket.MessageText, b)
	}
	if write(map[string]any{"type": "auth_required", "ha_version": s.info.Version}) != nil {
		return false
	}
	rctx, cancel := context.WithTimeout(ctx, wsAuthTimeout)
	defer cancel()
	_, data, err := conn.Read(rctx)
	if err != nil {
		return false
	}
	var msg struct {
		Type        string `json:"type"`
		AccessToken string `json:"access_token"`
	}
	ok := false
	if json.Unmarshal(data, &msg) == nil && msg.Type == "auth" {
		switch {
		case !s.auth.HasPassword():
			ok = true
		case msg.AccessToken != "":
			_, ok = s.auth.ValidateToken(msg.AccessToken)
		default:
			// The built-in UI has no token, only the login cookie.
			ok = s.authenticated(r)
		}
	}
	if !ok {
		write(map[string]any{"type": "auth_invalid", "message": "Invalid access token or password"})
		conn.Close(websocket.StatusPolicyViolation, "auth invalid")
		return false
	}
	return write(map[string]any{"type": "auth_ok", "ha_version": s.info.Version}) == nil
}

// wsMarshal encodes without HTML escaping, like HA does.
func wsMarshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func (c *wsConn) writeLoop(conn *websocket.Conn) {
	for {
		var msg any
		select {
		case <-c.ctx.Done():
			return
		case msg = <-c.out:
		}
		b, err := wsMarshal(msg)
		if err != nil {
			c.s.log.Error("websocket: encoding failed", "error", err)
			// A command must never go unanswered, or the client waits forever.
			m, ok := msg.(map[string]any)
			if !ok || m["id"] == nil || m["type"] != "result" {
				continue
			}
			b, err = wsMarshal(wsFailure(m["id"], &wsError{"unknown_error", "result could not be encoded: " + err.Error()}))
			if err != nil {
				continue
			}
		}
		wctx, cancel := context.WithTimeout(c.ctx, wsWriteTimeout)
		err = conn.Write(wctx, websocket.MessageText, b)
		cancel()
		if err != nil {
			c.cancel()
			return
		}
	}
}

// sendBlocking queues a reply; replies must not be lost, so it waits for
// room (the reader goroutine waiting is harmless, unlike the event bus).
func (c *wsConn) sendBlocking(msg any) {
	select {
	case c.out <- msg:
	case <-c.ctx.Done():
	}
}

// sendEvent queues an event without blocking the firing goroutine.
func (c *wsConn) sendEvent(id int, event any) {
	msg := map[string]any{"id": id, "type": "event", "event": event}
	select {
	case c.out <- msg:
	default:
		c.s.log.Warn("websocket: client too slow, event dropped", "subscription", id)
	}
}

func wsFailure(id any, err *wsError) map[string]any {
	return map[string]any{"id": id, "type": "result", "success": false, "error": map[string]any{"code": err.code, "message": err.message}}
}

// handle decodes and dispatches one frame; false means the frame was not
// a JSON object and the connection must be closed.
func (c *wsConn) handle(data []byte) bool {
	var p map[string]any
	if err := json.Unmarshal(data, &p); err != nil || p == nil {
		return false
	}
	idf, ok := p["id"].(float64)
	if !ok || idf != math.Trunc(idf) || idf < 0 || idf > 1<<53 {
		c.sendBlocking(wsFailure(0, wsErrf("invalid_format", "Message incorrectly formatted: id must be an integer")))
		return true
	}
	m := &wsMsg{ID: int(idf), P: p}
	m.Type, _ = p["type"].(string)
	if m.ID <= c.lastID {
		c.sendBlocking(wsFailure(m.ID, wsErrf("id_reuse", "Identifier values have to increase.")))
		return true
	}
	c.lastID = m.ID
	cmd, known := wsCommands[m.Type]
	if !known {
		c.sendBlocking(wsFailure(m.ID, wsErrf("unknown_command", "Unknown command.")))
		return true
	}
	if cmd.async {
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			c.execute(cmd, m)
		}()
		return true
	}
	c.execute(cmd, m)
	return true
}

func (c *wsConn) execute(cmd wsCommand, m *wsMsg) {
	res, err := cmd.run(c, m)
	if err != nil {
		var we *wsError
		if !errors.As(err, &we) {
			we = &wsError{"home_assistant_error", err.Error()}
		}
		c.sendBlocking(wsFailure(m.ID, we))
		return
	}
	if _, pong := res.(wsPong); pong {
		c.sendBlocking(map[string]any{"id": m.ID, "type": "pong"})
	} else {
		c.sendBlocking(map[string]any{"id": m.ID, "type": "result", "success": true, "result": res})
	}
	if m.after != nil {
		m.after()
	}
}

// --- subscriptions ---

func (c *wsConn) addSub(id int, stop func()) {
	c.mu.Lock()
	c.subs[id] = stop
	c.mu.Unlock()
}

func (c *wsConn) closeSubs() {
	c.mu.Lock()
	subs := c.subs
	c.subs = map[int]func(){}
	c.mu.Unlock()
	for _, stop := range subs {
		stop()
	}
}

// subscription turns core events into event messages. Until the result of
// the subscribing command is queued it only buffers, so the client never
// sees an event before the result that announces it.
type subscription struct {
	c       *wsConn
	id      int
	render  func(core.Event) (any, bool) // called with mu held
	initial func() any                   // optional first event, called with mu held

	mu      sync.Mutex
	ready   bool
	pending []core.Event
}

func (sb *subscription) push(ev core.Event) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	if !sb.ready {
		if len(sb.pending) >= wsQueueSize {
			sb.c.s.log.Warn("websocket: subscription backlog full, event dropped", "subscription", sb.id)
			return
		}
		sb.pending = append(sb.pending, ev)
		return
	}
	sb.emit(ev)
}

func (sb *subscription) emit(ev core.Event) {
	if payload, ok := sb.render(ev); ok {
		sb.c.sendEvent(sb.id, payload)
	}
}

// activate sends the initial event, then everything that arrived since
// subscribing. The initial snapshot is taken after subscribing so no
// change falls into the gap; duplicates are folded by the renderer.
func (sb *subscription) activate() {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	if sb.initial != nil {
		sb.c.sendEvent(sb.id, sb.initial())
	}
	for _, ev := range sb.pending {
		sb.emit(ev)
	}
	sb.pending = nil
	sb.ready = true
}

// watch subscribes to the core bus (typ "" = all) and registers the stop
// function under the command id.
func (c *wsConn) watch(m *wsMsg, typ string, render func(core.Event) (any, bool), initial func() any) *subscription {
	sb := &subscription{c: c, id: m.ID, render: render, initial: initial}
	c.addSub(m.ID, c.s.core.Subscribe(typ, sb.push))
	m.after = sb.activate
	return sb
}

// --- simple commands ---

func (c *wsConn) cmdPing(m *wsMsg) (any, error) { return wsPong{}, nil }

func (c *wsConn) cmdSupportedFeatures(m *wsMsg) (any, error) {
	// coalesce_messages is the only feature; we send one message per event
	// regardless, so there is nothing to switch on.
	return nil, nil
}

func (c *wsConn) cmdCurrentUser(m *wsMsg) (any, error) {
	return map[string]any{"id": "admin", "name": "Administrator", "is_owner": true, "is_admin": true, "credentials": []any{}, "mfa_modules": []any{}}, nil
}

func (c *wsConn) cmdGetStates(m *wsMsg) (any, error) {
	states := c.s.core.States()
	out := make([]map[string]any, 0, len(states))
	for _, st := range states {
		out = append(out, stateJSON(st))
	}
	return out, nil
}

func (c *wsConn) cmdGetConfig(m *wsMsg) (any, error) { return c.s.configData(), nil }

// cmdGetServices reshapes core's domain list into HA's nested object.
func (c *wsConn) cmdGetServices(m *wsMsg) (any, error) {
	out := map[string]any{}
	for _, entry := range c.s.core.Services() {
		domain, _ := entry["domain"].(string)
		svcs, ok := entry["services"].(map[string]core.ServiceDesc)
		if domain == "" || !ok {
			return nil, fmt.Errorf("unexpected service listing for domain %q", domain)
		}
		byName := make(map[string]any, len(svcs))
		for name, desc := range svcs {
			byName[name] = desc
		}
		out[domain] = byName
	}
	return out, nil
}

func (c *wsConn) cmdUnsubscribe(m *wsMsg) (any, error) {
	f, ok := m.P["subscription"].(float64)
	if !ok {
		return nil, wsErrf("invalid_format", "subscription must be an integer")
	}
	id := int(f)
	c.mu.Lock()
	stop, found := c.subs[id]
	delete(c.subs, id)
	c.mu.Unlock()
	if !found {
		return nil, wsErrf("not_found", "Subscription not found.")
	}
	stop()
	return nil, nil
}

func (c *wsConn) cmdCallService(m *wsMsg) (any, error) {
	domain, err := m.str("domain")
	if err != nil {
		return nil, err
	}
	service, err := m.str("service")
	if err != nil {
		return nil, err
	}
	in, err := m.obj("service_data")
	if err != nil {
		return nil, err
	}
	target, err := m.obj("target")
	if err != nil {
		return nil, err
	}
	// Core reads targets from the data map; copy so the decoded message is
	// not modified.
	data := make(map[string]any, len(in)+1)
	for k, v := range in {
		data[k] = v
	}
	if target != nil {
		data["target"] = target
	}
	cctx := core.NewContext()
	if _, err := c.s.core.CallService(c.ctx, domain, service, data, cctx); err != nil {
		var nf *core.ServiceNotFound
		if errors.As(err, &nf) {
			return nil, &wsError{"not_found", nf.Error()}
		}
		return nil, err
	}
	return map[string]any{"context": cctx, "response": nil}, nil
}

// --- events ---

func stateOrNil(v any) any {
	if st, ok := v.(*core.State); ok && st != nil {
		return stateJSON(st)
	}
	return nil
}

func eventJSON(ev core.Event) map[string]any {
	data := ev.Data
	if ev.Type == "state_changed" {
		data = map[string]any{"entity_id": ev.Data["entity_id"], "old_state": stateOrNil(ev.Data["old_state"]), "new_state": stateOrNil(ev.Data["new_state"])}
	}
	if data == nil {
		data = map[string]any{}
	}
	return map[string]any{"event_type": ev.Type, "data": data, "origin": ev.Origin, "time_fired": haTime(ev.TimeFired), "context": ev.Context}
}

func (c *wsConn) cmdSubscribeEvents(m *wsMsg) (any, error) {
	typ, err := m.optStr("event_type")
	if err != nil {
		return nil, err
	}
	c.watch(m, typ, func(ev core.Event) (any, bool) { return eventJSON(ev), true }, nil)
	return nil, nil
}

// --- compressed entity states ---

// compactState is what subscribe_entities remembers per entity so that
// later events can be sent as differences.
type compactState struct {
	state  string
	attrs  map[string]any
	ctx    string
	lc, lu float64
}

func unixSeconds(t time.Time) float64 { return float64(t.UnixNano()) / 1e9 }

func compactOf(st *core.State) compactState {
	return compactState{st.State, st.Attributes, st.Context.ID, unixSeconds(st.LastChanged), unixSeconds(st.LastUpdated)}
}

func (cs compactState) full() map[string]any {
	attrs := cs.attrs
	if attrs == nil {
		attrs = map[string]any{}
	}
	return map[string]any{"s": cs.state, "a": attrs, "c": cs.ctx, "lc": cs.lc, "lu": cs.lu}
}

// diff returns HA's {"+": changed, "-": removed} form, or nil when the
// two are identical.
func (cs compactState) diff(old compactState) map[string]any {
	plus := map[string]any{}
	if cs.state != old.state {
		plus["s"] = cs.state
	}
	changed := map[string]any{}
	for k, v := range cs.attrs {
		if ov, ok := old.attrs[k]; !ok || !reflect.DeepEqual(ov, v) {
			changed[k] = v
		}
	}
	if len(changed) > 0 {
		plus["a"] = changed
	}
	if cs.ctx != old.ctx {
		plus["c"] = cs.ctx
	}
	if cs.lc != old.lc {
		plus["lc"] = cs.lc
	}
	if cs.lu != old.lu {
		plus["lu"] = cs.lu
	}
	removed := []string{}
	for k := range old.attrs {
		if _, ok := cs.attrs[k]; !ok {
			removed = append(removed, k)
		}
	}
	out := map[string]any{}
	if len(plus) > 0 {
		out["+"] = plus
	}
	if len(removed) > 0 {
		out["-"] = map[string]any{"a": removed}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (c *wsConn) cmdSubscribeEntities(m *wsMsg) (any, error) {
	ids, err := m.strList("entity_ids")
	if err != nil {
		return nil, err
	}
	var only map[string]bool
	if ids != nil {
		only = make(map[string]bool, len(ids))
		for _, id := range ids {
			only[id] = true
		}
	}
	wanted := func(id string) bool { return only == nil || only[id] }
	sent := map[string]compactState{}

	render := func(ev core.Event) (any, bool) {
		id, _ := ev.Data["entity_id"].(string)
		if !wanted(id) {
			return nil, false
		}
		ns, _ := ev.Data["new_state"].(*core.State)
		old, known := sent[id]
		if ns == nil {
			if !known {
				return nil, false
			}
			delete(sent, id)
			return map[string]any{"r": []string{id}}, true
		}
		cur := compactOf(ns)
		sent[id] = cur
		if !known {
			return map[string]any{"a": map[string]any{id: cur.full()}}, true
		}
		d := cur.diff(old)
		if d == nil {
			return nil, false
		}
		return map[string]any{"c": map[string]any{id: d}}, true
	}
	initial := func() any {
		all := map[string]any{}
		for _, st := range c.s.core.States() {
			if wanted(st.EntityID) {
				cs := compactOf(st)
				sent[st.EntityID] = cs
				all[st.EntityID] = cs.full()
			}
		}
		return map[string]any{"a": all}
	}
	c.watch(m, "state_changed", render, initial)
	return nil, nil
}

// --- registries ---

// wsEntityEntry adds the "id" key HA clients expect to the entry.
type wsEntityEntry struct {
	*core.EntityEntry
	ID string `json:"id"`
}

func entityJSON(e *core.EntityEntry) wsEntityEntry { return wsEntityEntry{e, e.EntityID} }

func (c *wsConn) cmdEntityList(m *wsMsg) (any, error) {
	entries := c.s.core.Entities()
	out := make([]wsEntityEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, entityJSON(e))
	}
	return out, nil
}

func (c *wsConn) cmdEntityGet(m *wsMsg) (any, error) {
	id, err := m.str("entity_id")
	if err != nil {
		return nil, err
	}
	e := c.s.core.Entity(id)
	if e == nil {
		return nil, wsErrf("not_found", "Entity %s not found", id)
	}
	return entityJSON(e), nil
}

func (c *wsConn) cmdEntityUpdate(m *wsMsg) (any, error) {
	id, err := m.str("entity_id")
	if err != nil {
		return nil, err
	}
	upd, err := m.pick("name", "icon", "area_id", "new_entity_id", "disabled_by", "hidden_by")
	if err != nil {
		return nil, err
	}
	if c.s.core.Entity(id) == nil {
		return nil, wsErrf("not_found", "Entity %s not found", id)
	}
	e, err := c.s.core.UpdateEntity(id, upd)
	if err != nil {
		return nil, err
	}
	return map[string]any{"entity_entry": entityJSON(e)}, nil
}

func (c *wsConn) cmdDeviceList(m *wsMsg) (any, error) { return c.s.core.Devices(), nil }

func (c *wsConn) cmdDeviceUpdate(m *wsMsg) (any, error) {
	id, err := m.str("device_id")
	if err != nil {
		return nil, err
	}
	upd, err := m.pick("name_by_user", "area_id", "disabled_by")
	if err != nil {
		return nil, err
	}
	if c.s.core.Device(id) == nil {
		return nil, wsErrf("not_found", "Device %s not found", id)
	}
	return c.s.core.UpdateDevice(id, upd)
}

func (c *wsConn) cmdAreaList(m *wsMsg) (any, error) { return c.s.core.Areas(), nil }

func (c *wsConn) cmdAreaCreate(m *wsMsg) (any, error) {
	name, err := m.str("name")
	if err != nil {
		return nil, err
	}
	return c.s.core.CreateArea(name)
}

func (c *wsConn) hasArea(id string) bool {
	for _, a := range c.s.core.Areas() {
		if a.ID == id {
			return true
		}
	}
	return false
}

func (c *wsConn) cmdAreaUpdate(m *wsMsg) (any, error) {
	id, err := m.str("area_id")
	if err != nil {
		return nil, err
	}
	upd, err := m.pick("name", "icon", "picture")
	if err != nil {
		return nil, err
	}
	if !c.hasArea(id) {
		return nil, wsErrf("not_found", "Area %s not found", id)
	}
	return c.s.core.UpdateArea(id, upd)
}

func (c *wsConn) cmdAreaDelete(m *wsMsg) (any, error) {
	id, err := m.str("area_id")
	if err != nil {
		return nil, err
	}
	if !c.hasArea(id) {
		return nil, wsErrf("not_found", "Area %s not found", id)
	}
	return nil, c.s.core.DeleteArea(id)
}

// --- templates and automation helpers ---

func (c *wsConn) cmdRenderTemplate(m *wsMsg) (any, error) {
	src, err := m.str("template")
	if err != nil {
		return nil, err
	}
	vars, err := m.obj("variables")
	if err != nil {
		return nil, err
	}
	tpl, err := template.Parse(src)
	if err != nil {
		return nil, &wsError{"template_error", err.Error()}
	}
	var env *template.Env
	if c.s.Automations != nil {
		env = c.s.Automations.TemplateEnv()
	}
	first, err := tpl.Render(vars, env)
	if err != nil {
		return nil, &wsError{"template_error", err.Error()}
	}

	ctx, cancel := context.WithCancel(c.ctx)
	// The bus handler only flags work; rendering happens on a goroutine of
	// its own so a slow template cannot stall whoever changed a state.
	dirty := make(chan struct{}, 1)
	unsub := c.s.core.Subscribe("state_changed", func(core.Event) {
		select {
		case dirty <- struct{}{}:
		default:
		}
	})
	c.addSub(m.ID, func() {
		unsub()
		cancel()
	})
	result := func(out string) map[string]any {
		return map[string]any{"result": out, "listeners": map[string]any{"all": false, "entities": []string{}, "domains": []string{}, "time": false}}
	}
	m.after = func() {
		c.sendBlocking(map[string]any{"id": m.ID, "type": "event", "event": result(first)})
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			last := first
			for {
				select {
				case <-ctx.Done():
					return
				case <-dirty:
				}
				out, err := tpl.Render(vars, env)
				if err != nil {
					c.sendEvent(m.ID, map[string]any{"error": err.Error()})
					continue
				}
				// HA only republishes when the rendered text changed.
				if out == last {
					continue
				}
				last = out
				c.sendEvent(m.ID, result(out))
			}
		}()
	}
	return nil, nil
}

func (c *wsConn) automations() (AutomationSource, error) {
	if c.s.Automations == nil {
		return nil, wsErrf("unknown_command", "Automations are not enabled.")
	}
	return c.s.Automations, nil
}

func (c *wsConn) cmdSubscribeTrigger(m *wsMsg) (any, error) {
	src, err := c.automations()
	if err != nil {
		return nil, err
	}
	cfg, err := m.obj("trigger")
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, wsErrf("invalid_format", "trigger is required")
	}
	sb := &subscription{c: c, id: m.ID, render: func(ev core.Event) (any, bool) {
		return map[string]any{"variables": map[string]any{"trigger": ev.Data}, "context": ev.Context}, true
	}}
	stop, err := src.SubscribeTrigger(cfg, func(vars map[string]any) {
		sb.push(core.Event{Data: vars, Context: core.NewContext()})
	})
	if err != nil {
		return nil, err
	}
	c.addSub(m.ID, stop)
	m.after = sb.activate
	return nil, nil
}

func (c *wsConn) cmdValidateConfig(m *wsMsg) (any, error) {
	src, err := c.automations()
	if err != nil {
		return nil, err
	}
	return src.ValidateConfig(m.P["triggers"], m.P["conditions"], m.P["actions"]), nil
}

// --- history, statistics, logbook ---

func (m *wsMsg) optBool(key string, def bool) (bool, error) {
	v, present := m.P[key]
	if !present || v == nil {
		return def, nil
	}
	b, ok := v.(bool)
	if !ok {
		return false, wsErrf("invalid_format", "%s must be a boolean", key)
	}
	return b, nil
}

// time reads an ISO 8601 timestamp; an absent or null optional key
// yields the zero time.
func (m *wsMsg) time(key string, required bool) (time.Time, error) {
	v, present := m.P[key]
	if !present || v == nil {
		if required {
			return time.Time{}, wsErrf("invalid_format", "%s is required", key)
		}
		return time.Time{}, nil
	}
	s, ok := v.(string)
	if !ok {
		return time.Time{}, wsErrf("invalid_format", "%s must be a string", key)
	}
	t, ok := parseTimestamp(s)
	if !ok {
		return time.Time{}, wsErrf("invalid_format", "%s is not an ISO 8601 timestamp", key)
	}
	return t, nil
}

func (c *wsConn) history() (HistorySource, error) {
	if c.s.History == nil {
		return nil, wsErrf("not_supported", "The recorder is not enabled.")
	}
	return c.s.History, nil
}

// windowEnd is the end of a query window. The window is half open, so
// the default end ("now") is pushed one microsecond out to keep a state
// written at exactly this instant.
func (c *wsConn) windowEnd(m *wsMsg) (time.Time, error) {
	end, err := m.time("end_time", false)
	if err != nil {
		return end, err
	}
	if end.IsZero() {
		end = c.s.core.Now().Add(time.Microsecond)
	}
	return end, nil
}

var statisticsTypes = map[string]bool{"mean": true, "min": true, "max": true, "state": true, "sum": true}

func (c *wsConn) cmdStatisticsDuringPeriod(m *wsMsg) (any, error) {
	h, err := c.history()
	if err != nil {
		return nil, err
	}
	start, err := m.time("start_time", true)
	if err != nil {
		return nil, err
	}
	end, err := m.time("end_time", false)
	if err != nil {
		return nil, err
	}
	ids, err := m.strList("statistic_ids")
	if err != nil {
		return nil, err
	}
	if ids == nil {
		return nil, wsErrf("invalid_format", "statistic_ids is required")
	}
	period, err := m.str("period")
	if err != nil {
		return nil, err
	}
	if !validStatisticsPeriod(period) {
		return nil, wsErrf("invalid_format", "period must be hour, day, week or month")
	}
	types, err := m.strList("types")
	if err != nil {
		return nil, err
	}
	for _, t := range types {
		if !statisticsTypes[t] {
			return nil, wsErrf("not_supported", "statistics type %q is not supported", t)
		}
	}
	res, err := h.Statistics(start, end, ids, period)
	if err != nil {
		return nil, err
	}
	out := make(map[string]any, len(res))
	for id, rows := range res {
		list := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			item, err := statisticsItem(row, types)
			if err != nil {
				return nil, err
			}
			list = append(list, item)
		}
		out[id] = list
	}
	return out, nil
}

// statisticsItem turns a recorder row (ISO bounds) into the frontend's
// shape: start and end as epoch milliseconds, only the requested types.
func statisticsItem(row map[string]any, types []string) (map[string]any, error) {
	item := map[string]any{}
	for _, k := range []string{"start", "end"} {
		s, _ := row[k].(string)
		t, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return nil, fmt.Errorf("statistics %s %q: %w", k, s, err)
		}
		item[k] = float64(t.UnixMilli())
	}
	if len(types) == 0 {
		types = []string{"mean", "min", "max", "state", "sum"}
	}
	for _, t := range types {
		item[t] = row[t]
	}
	return item, nil
}

func (c *wsConn) cmdStatisticsMetadata(m *wsMsg) (any, error) {
	h, err := c.history()
	if err != nil {
		return nil, err
	}
	ids, err := m.strList("statistic_ids")
	if err != nil {
		return nil, err
	}
	return h.StatisticsMetadata(ids), nil
}

func (c *wsConn) cmdRecorderInfo(m *wsMsg) (any, error) {
	return map[string]any{"migration_in_progress": false, "recording": c.s.History != nil, "thread_running": true, "backlog": 0}, nil
}

// parseHATime reads the timestamps the recorder formats for REST.
func parseHATime(v any) (time.Time, error) {
	s, _ := v.(string)
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return t, fmt.Errorf("recorder timestamp %q: %w", s, err)
	}
	return t, nil
}

func (c *wsConn) cmdHistoryDuringPeriod(m *wsMsg) (any, error) {
	h, err := c.history()
	if err != nil {
		return nil, err
	}
	start, err := m.time("start_time", true)
	if err != nil {
		return nil, err
	}
	end, err := c.windowEnd(m)
	if err != nil {
		return nil, err
	}
	ids, err := m.strList("entity_ids")
	if err != nil {
		return nil, err
	}
	minimal, err := m.optBool("minimal_response", false)
	if err != nil {
		return nil, err
	}
	// HA defaults to significant changes only on this command.
	significant, err := m.optBool("significant_changes_only", true)
	if err != nil {
		return nil, err
	}
	noAttrs, err := m.optBool("no_attributes", false)
	if err != nil {
		return nil, err
	}
	// The recorder omits attributes for minimal responses, which is
	// also what no_attributes asks for.
	lists, err := h.History(start, end, ids, minimal || noAttrs, significant)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	for _, list := range lists {
		if len(list) == 0 {
			continue
		}
		entityID, _ := list[0]["entity_id"].(string)
		compact := make([]map[string]any, 0, len(list))
		for _, item := range list {
			lu, err := parseHATime(item["last_updated"])
			if err != nil {
				return nil, err
			}
			lc, err := parseHATime(item["last_changed"])
			if err != nil {
				return nil, err
			}
			ci := map[string]any{"s": item["state"], "lu": unixSeconds(lu)}
			if !lc.Equal(lu) {
				ci["lc"] = unixSeconds(lc)
			}
			if a, ok := item["attributes"]; ok {
				ci["a"] = a
			}
			compact = append(compact, ci)
		}
		out[entityID] = compact
	}
	return out, nil
}

func (c *wsConn) cmdLogbookEvents(m *wsMsg) (any, error) {
	h, err := c.history()
	if err != nil {
		return nil, err
	}
	start, err := m.time("start_time", true)
	if err != nil {
		return nil, err
	}
	end, err := c.windowEnd(m)
	if err != nil {
		return nil, err
	}
	ids, err := m.strList("entity_ids")
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		ids = []string{""}
	}
	var entries []map[string]any
	for _, id := range ids {
		part, err := h.Logbook(start, end, id)
		if err != nil {
			return nil, err
		}
		entries = append(entries, part...)
	}
	type stamped struct {
		when float64
		item map[string]any
	}
	list := make([]stamped, 0, len(entries))
	for _, e := range entries {
		t, err := parseHATime(e["when"])
		if err != nil {
			return nil, err
		}
		item := make(map[string]any, len(e))
		for k, v := range e {
			item[k] = v
		}
		item["when"] = unixSeconds(t)
		list = append(list, stamped{unixSeconds(t), item})
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].when < list[j].when })
	out := make([]map[string]any, 0, len(list))
	for _, s := range list {
		out = append(out, s.item)
	}
	return out, nil
}

// --- persistent notifications ---

func (c *wsConn) cmdNotificationsGet(m *wsMsg) (any, error) {
	return c.s.core.Notifications(), nil
}

func (c *wsConn) cmdNotificationsSubscribe(m *wsMsg) (any, error) {
	list := func(typ string) any {
		return map[string]any{"type": typ, "notifications": c.s.core.Notifications()}
	}
	c.watch(m, "persistent_notifications_updated", func(ev core.Event) (any, bool) {
		typ, _ := ev.Data["type"].(string)
		if typ != "added" && typ != "removed" {
			return nil, false
		}
		return list(typ), true
	}, func() any { return list("added") })
	return nil, nil
}

// --- dashboard and frontend support ---

func (c *wsConn) cmdLovelaceConfig(m *wsMsg) (any, error) {
	// Only the default dashboard exists.
	if p, err := m.optStr("url_path"); err != nil {
		return nil, err
	} else if p != "" {
		return nil, wsErrf("config_not_found", "No dashboard %q.", p)
	}
	cfg, err := c.s.loadDashboard()
	var invalid *dashboardError
	switch {
	case errors.Is(err, errDashboardMissing):
		return nil, wsErrf("config_not_found", "No config found.")
	case errors.As(err, &invalid):
		return nil, wsErrf("invalid_config", "%s", err.Error())
	case err != nil:
		return nil, err
	}
	return cfg, nil
}

func (c *wsConn) cmdLovelaceResources(m *wsMsg) (any, error) { return []any{}, nil }

func (c *wsConn) cmdGetThemes(m *wsMsg) (any, error) {
	return map[string]any{"themes": map[string]any{}, "default_theme": "default"}, nil
}

func (c *wsConn) cmdSystemHealth(m *wsMsg) (any, error) { return map[string]any{}, nil }

// cmdCheckConfig validates the files the engine loads; a missing file
// is fine, an empty install has none.
func (c *wsConn) cmdCheckConfig(m *wsMsg) (any, error) {
	var problems []string
	if dir := c.s.info.ConfigDir; dir != "" {
		for _, name := range []string{"automations.yaml", "scripts.yaml", "scenes.yaml"} {
			data, err := os.ReadFile(filepath.Join(dir, name))
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if err := automation.ValidateWithBlueprints(dir, name, data); err != nil {
				problems = append(problems, name+": "+err.Error())
			}
		}
	}
	if len(problems) > 0 {
		return map[string]any{"result": "invalid", "errors": strings.Join(problems, "\n")}, nil
	}
	return map[string]any{"result": "valid", "errors": nil}, nil
}
