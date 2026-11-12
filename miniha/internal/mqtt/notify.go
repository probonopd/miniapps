/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package mqtt

import (
	"context"

	"github.com/probonopd/miniha/internal/template"
)

func init() {
	components["notify"] = newNotify
}

type mqttNotify struct {
	e        *entity
	cmdTopic string
	cmdTpl   *template.Template
}

func newNotify(e *entity) (component, error) {
	if !e.cfg.Has("command_topic") {
		return nil, errMissing("command_topic")
	}
	tpl, err := e.cfg.Template("command_template")
	if err != nil {
		return nil, err
	}
	n := &mqttNotify{e: e, cmdTopic: e.cfg.Str("command_topic"), cmdTpl: tpl}
	e.d.core.RegisterController(e.entityID, n)
	e.write(false)
	return n, nil
}

func (n *mqttNotify) stop() {}

func (n *mqttNotify) SendMessage(ctx context.Context, message, title string) error {
	payload := message
	if n.cmdTpl != nil {
		var err error
		payload, err = n.cmdTpl.Render(map[string]any{
			"value": message, "message": message, "title": title,
			"entity_id": n.e.entityID, "name": n.e.d.core.FriendlyName(n.e.entityID),
		}, nil)
		if err != nil {
			return err
		}
	}
	if err := n.e.publish(n.cmdTopic, payload); err != nil {
		return err
	}
	n.e.setState(activationTimestamp(n.e.d.core.Now()), ctx)
	return nil
}
