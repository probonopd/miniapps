/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package mqtt

import (
	"context"
	"time"
)

func init() {
	components["scene"] = newScene
}

// activationTimestamp is the state format HA uses for scenes, notifiers
// and images: the time of the last activation, in UTC.
func activationTimestamp(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000000-07:00")
}

type mqttScene struct {
	e         *entity
	cmdTopic  string
	payloadOn string
}

func newScene(e *entity) (component, error) {
	if !e.cfg.Has("command_topic") {
		return nil, errMissing("command_topic")
	}
	s := &mqttScene{e: e, cmdTopic: e.cfg.Str("command_topic"), payloadOn: e.cfg.StrDef("payload_on", "ON")}
	e.d.core.RegisterController(e.entityID, s)
	e.write(false)
	return s, nil
}

func (s *mqttScene) stop() {}

func (s *mqttScene) TurnOn(ctx context.Context, _ map[string]any) error {
	if err := s.e.publish(s.cmdTopic, s.payloadOn); err != nil {
		return err
	}
	s.e.setState(activationTimestamp(s.e.d.core.Now()), ctx)
	return nil
}

// TurnOff is a no-op because a scene has no off state; HA ignores it too.
func (s *mqttScene) TurnOff(context.Context, map[string]any) error { return nil }
