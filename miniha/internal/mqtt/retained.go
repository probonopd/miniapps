/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package mqtt

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	mochi "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/storage"
	"github.com/mochi-mqtt/server/v2/packets"
	"github.com/probonopd/miniha/internal/store"
)

// retainedFlushDelay batches bursts of retained updates (a bridge
// re-announcing all its discovery configs) into one write, which keeps
// SD card wear low.
const retainedFlushDelay = 2 * time.Second

// retainedHook persists retained messages to one JSON file so discovery
// configs survive a restart. The stock storage hooks need a database and
// also persist clients, subscriptions and inflight messages, none of
// which MiniHA wants on a small device.
type retainedHook struct {
	mochi.HookBase
	path string

	mu    sync.Mutex
	msgs  map[string]storage.Message // topic -> message
	timer *time.Timer
	dirty bool
}

func newRetainedHook(path string) *retainedHook {
	return &retainedHook{path: path, msgs: map[string]storage.Message{}}
}

func (h *retainedHook) ID() string { return "miniha-retained" }

func (h *retainedHook) Provides(b byte) bool {
	switch b {
	case mochi.OnRetainMessage, mochi.OnRetainedExpired, mochi.StoredRetainedMessages:
		return true
	}
	return false
}

// Init loads the file. A corrupt file is an error instead of being
// dropped, because silently discarding retained configs would make
// entities vanish without explanation.
func (h *retainedHook) Init(any) error {
	data, err := os.ReadFile(h.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var list []storage.Message
	if err := json.Unmarshal(data, &list); err != nil {
		return fmt.Errorf("%s: %w", h.path, err)
	}
	for _, m := range list {
		h.msgs[m.TopicName] = m
	}
	return nil
}

func (h *retainedHook) StoredRetainedMessages() ([]storage.Message, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]storage.Message, 0, len(h.msgs))
	for _, m := range h.msgs {
		out = append(out, m)
	}
	return out, nil
}

// OnRetainMessage: r == -1 means the retained message was cleared by an
// empty payload.
func (h *retainedHook) OnRetainMessage(cl *mochi.Client, pk packets.Packet, r int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if r == -1 {
		delete(h.msgs, pk.TopicName)
		h.scheduleLocked()
		return
	}
	props := pk.Properties.Copy(false)
	m := storage.Message{
		ID:          "RET_" + pk.TopicName,
		T:           storage.RetainedKey,
		FixedHeader: pk.FixedHeader,
		TopicName:   pk.TopicName,
		Payload:     append([]byte(nil), pk.Payload...),
		Created:     pk.Created,
		Origin:      pk.Origin,
		Properties: storage.MessageProperties{
			PayloadFormat:          props.PayloadFormat,
			PayloadFormatFlag:      props.PayloadFormatFlag,
			MessageExpiryInterval:  props.MessageExpiryInterval,
			ContentType:            props.ContentType,
			ResponseTopic:          props.ResponseTopic,
			CorrelationData:        props.CorrelationData,
			SubscriptionIdentifier: props.SubscriptionIdentifier,
			TopicAlias:             props.TopicAlias,
			User:                   props.User,
		},
	}
	if cl != nil {
		m.Client = cl.ID
	}
	h.msgs[pk.TopicName] = m
	h.scheduleLocked()
}

func (h *retainedHook) OnRetainedExpired(filter string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.msgs, filter)
	h.scheduleLocked()
}

func (h *retainedHook) scheduleLocked() {
	h.dirty = true
	if h.timer == nil {
		h.timer = time.AfterFunc(retainedFlushDelay, func() {
			if err := h.flush(); err != nil {
				h.Log.Error("writing retained messages failed", "path", h.path, "err", err)
			}
		})
	}
}

// Stop writes pending changes so a clean shutdown loses nothing.
func (h *retainedHook) Stop() error {
	h.mu.Lock()
	if h.timer != nil {
		h.timer.Stop()
		h.timer = nil
	}
	h.mu.Unlock()
	return h.flush()
}

func (h *retainedHook) flush() error {
	h.mu.Lock()
	h.timer = nil
	if !h.dirty {
		h.mu.Unlock()
		return nil
	}
	list := make([]storage.Message, 0, len(h.msgs))
	for _, m := range h.msgs {
		list = append(list, m)
	}
	h.dirty = false
	h.mu.Unlock()
	data, err := json.Marshal(list)
	if err != nil {
		return err
	}
	if err := store.WriteAtomic(h.path, data, 0o644); err != nil {
		h.mu.Lock()
		h.dirty = true
		h.mu.Unlock()
		return err
	}
	return nil
}
