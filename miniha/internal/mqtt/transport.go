/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

// Package mqtt implements the HA MQTT integration: discovery, the entity
// components and the embedded broker.
package mqtt

import "strings"

type Message struct {
	Topic    string
	Payload  []byte
	Retained bool
}

// Transport is what the entities need from a broker. The embedded broker
// implements it; tests use a fake.
type Transport interface {
	Subscribe(filter string, cb func(Message)) (unsubscribe func(), err error)
	Publish(topic string, payload []byte, qos byte, retain bool) error
}

// MatchTopic implements MQTT filter matching with + and # wildcards.
func MatchTopic(filter, topic string) bool {
	f := strings.Split(filter, "/")
	t := strings.Split(topic, "/")
	for i, part := range f {
		if part == "#" {
			return true
		}
		if i >= len(t) {
			return false
		}
		if part != "+" && part != t[i] {
			return false
		}
	}
	return len(f) == len(t)
}
