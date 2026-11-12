/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package mqtt

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/probonopd/miniha/internal/template"
)

func init() {
	components["alarm_control_panel"] = newAlarm
}

// Codes with these values are checked by the device itself, so the panel
// accepts any code and passes it along in the command_template.
const (
	remoteCode     = "REMOTE_CODE"
	remoteCodeText = "REMOTE_CODE_TEXT"
)

var digitsOnly = regexp.MustCompile(`^\d+$`)

var alarmStates = map[string]bool{
	"disarmed": true, "armed_home": true, "armed_away": true, "armed_night": true,
	"armed_vacation": true, "armed_custom_bypass": true, "pending": true,
	"triggered": true, "arming": true, "disarming": true,
}

// alarmActions maps each supported_features name to its HA feature bit and
// the default command payload.
var alarmActions = []struct {
	feature string
	bit     int
	option  string
	payload string
}{
	{"arm_home", 1, "payload_arm_home", "ARM_HOME"},
	{"arm_away", 2, "payload_arm_away", "ARM_AWAY"},
	{"arm_night", 4, "payload_arm_night", "ARM_NIGHT"},
	{"trigger", 8, "payload_trigger", "TRIGGER"},
	{"arm_custom_bypass", 16, "payload_arm_custom_bypass", "ARM_CUSTOM_BYPASS"},
	{"arm_vacation", 32, "payload_arm_vacation", "ARM_VACATION"},
}

type mqttAlarm struct {
	e         *entity
	topic     string
	tpl       *template.Template
	disarm    string
	code      string
	hasCode   bool
	required  map[string]bool   // arm, disarm, trigger
	payloads  map[string]string // feature -> payload
	supported map[string]bool
	features  int
}

func newAlarm(e *entity) (component, error) {
	cfg := e.cfg
	for _, key := range []string{"state_topic", "command_topic"} {
		if !cfg.Has(key) {
			return nil, errMissing(key)
		}
	}
	tpl, err := cfg.Template("command_template")
	if err != nil {
		return nil, err
	}
	a := &mqttAlarm{
		e: e, topic: cfg.Str("command_topic"), tpl: tpl,
		disarm: cfg.StrDef("payload_disarm", "DISARM"),
		code:   cfg.Str("code"), hasCode: cfg.Has("code"),
		required: map[string]bool{
			"arm":     cfg.Bool("code_arm_required", true),
			"disarm":  cfg.Bool("code_disarm_required", true),
			"trigger": cfg.Bool("code_trigger_required", true),
		},
		payloads:  map[string]string{},
		supported: map[string]bool{},
	}
	names := cfg.Strings("supported_features")
	if !cfg.Has("supported_features") {
		for _, act := range alarmActions {
			names = append(names, act.feature)
		}
	}
	for _, name := range names {
		known := false
		for _, act := range alarmActions {
			if act.feature == name {
				known = true
				a.supported[name] = true
				a.features |= act.bit
			}
		}
		if !known {
			return nil, fmt.Errorf("unknown supported_features entry %q", name)
		}
	}
	for _, act := range alarmActions {
		a.payloads[act.feature] = cfg.StrDef(act.option, act.payload)
	}
	e.attrs = func() map[string]any {
		return map[string]any{
			"code_format":        a.codeFormat(),
			"code_arm_required":  a.required["arm"],
			"changed_by":         nil,
			"supported_features": a.features,
		}
	}
	if err := subscribeState(e, func(payload string) {
		if payload == "None" {
			e.setState("", context.Background())
			return
		}
		if !alarmStates[payload] {
			e.d.log.Warn("alarm_control_panel payload is not a valid state", "entity_id", e.entityID, "payload", payload)
			return
		}
		e.setState(payload, context.Background())
	}); err != nil {
		return nil, err
	}
	e.d.core.RegisterController(e.entityID, a)
	e.write(false)
	return a, nil
}

func (a *mqttAlarm) stop() {}

// codeFormat is what a frontend uses to pick its keypad; nil when the panel
// asks for no code.
func (a *mqttAlarm) codeFormat() any {
	switch {
	case !a.hasCode:
		return nil
	case a.code == remoteCode, digitsOnly.MatchString(a.code):
		return "number"
	}
	return "text"
}

// send checks the code for the given requirement class (arm, disarm,
// trigger) and publishes the action payload.
func (a *mqttAlarm) send(action, class, payload, code string) error {
	if class != "" && a.required[class] && a.hasCode && a.code != remoteCode && a.code != remoteCodeText && code != a.code {
		return fmt.Errorf("invalid code for %s", strings.ReplaceAll(action, "_", " "))
	}
	if a.tpl != nil {
		var err error
		payload, err = renderCmdVars(a.e, a.tpl, map[string]any{"action": payload, "code": codeVar(code)})
		if err != nil {
			return err
		}
	}
	return a.e.publish(a.topic, payload)
}

func (a *mqttAlarm) feature(name, class, code string) error {
	if !a.supported[name] {
		return fmt.Errorf("alarm_control_panel does not support %s", name)
	}
	return a.send(name, class, a.payloads[name], code)
}

func (a *mqttAlarm) AlarmDisarm(_ context.Context, code string) error {
	return a.send("disarm", "disarm", a.disarm, code)
}

func (a *mqttAlarm) AlarmArmHome(_ context.Context, code string) error {
	return a.feature("arm_home", "arm", code)
}

func (a *mqttAlarm) AlarmArmAway(_ context.Context, code string) error {
	return a.feature("arm_away", "arm", code)
}

func (a *mqttAlarm) AlarmArmNight(_ context.Context, code string) error {
	return a.feature("arm_night", "arm", code)
}

func (a *mqttAlarm) AlarmArmVacation(_ context.Context, code string) error {
	return a.feature("arm_vacation", "arm", code)
}

func (a *mqttAlarm) AlarmArmCustomBypass(_ context.Context, code string) error {
	return a.feature("arm_custom_bypass", "arm", code)
}

func (a *mqttAlarm) AlarmTrigger(_ context.Context, code string) error {
	return a.feature("trigger", "trigger", code)
}
