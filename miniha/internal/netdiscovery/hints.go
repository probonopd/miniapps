/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package netdiscovery

import "strings"

// Device kinds reported by Hint.
const (
	KindESPHome       = "esphome"
	KindShelly        = "shelly"
	KindTasmota       = "tasmota"
	KindHomeKit       = "homekit"
	KindCast          = "cast"
	KindHomeAssistant = "home_assistant"
	KindMQTTBroker    = "mqtt_broker"
	KindPrinter       = "printer"
	KindOther         = "other"
)

// normalizeService reduces what a resolver reports (case, trailing dot,
// ".local" domain) to the bare "_name._tcp" form used as a map key and in
// the service table.
func normalizeService(service string) string {
	s := strings.ToLower(strings.TrimSuffix(service, "."))
	s = strings.TrimSuffix(s, ".local")
	return s
}

// firstNonEmpty returns the first TXT value that is set; the order encodes
// which key says the most about the device.
func firstNonEmpty(txt map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := txt[k]; v != "" {
			return v
		}
	}
	return ""
}

// isTasmota reports whether a generic HTTP service is a Tasmota device,
// which advertises itself as _http._tcp with devicetype=tasmota.
func isTasmota(txt map[string]string) bool {
	return strings.EqualFold(txt["devicetype"], "tasmota")
}

// Hint classifies a discovered service and says how to connect it to MiniHA,
// whose only device protocol is MQTT. mqttHost is the address devices should
// point their MQTT client at. It is pure so the hint table can be tested
// without a network.
func Hint(service string, txt map[string]string, mqttHost string) (kind, model, hint string) {
	host := mqttHost
	if host == "" {
		host = "<MiniHA host>"
	}
	switch normalizeService(service) {
	case "_esphomelib._tcp":
		return KindESPHome, firstNonEmpty(txt, "project_name", "board", "platform"),
			"ESPHome device. Add the mqtt: component to its YAML with broker: " + host +
				" and discovery: true, or use its web UI if it has one."
	case "_shelly._tcp":
		model = firstNonEmpty(txt, "app")
		if _, gen2 := txt["gen"]; gen2 {
			if model == "" {
				model = "Gen" + txt["gen"]
			}
			return KindShelly, model,
				"Shelly Gen2+: enable MQTT in Settings > MQTT with server " + host +
					":1883 and RPC over MQTT; add a manual MQTT entity or a bridge, Shelly does not publish HA discovery itself."
		}
		return KindShelly, model,
			"Shelly Gen1 does not publish Home Assistant MQTT discovery; use an MQTT bridge such as Zigbee2MQTT-style gateways" +
				" or a manual MQTT entity in configuration.yaml with state topic shellies/<id>/relay/0."
	case "_http._tcp":
		if isTasmota(txt) {
			return KindTasmota, firstNonEmpty(txt, "model"),
				"Tasmota: run SetOption19 1 and MqttHost " + host + " in its console; MiniHA also reads Tasmota native discovery."
		}
	case "_hap._tcp":
		return KindHomeKit, firstNonEmpty(txt, "md"), "HomeKit accessory; no MQTT path. Not supported."
	case "_googlecast._tcp":
		return KindCast, firstNonEmpty(txt, "md"), "Google Cast device; no MQTT path. Not supported."
	case "_home-assistant._tcp":
		return KindHomeAssistant, "",
			"Another Home Assistant instance (version in TXT); it can consume this hub's entities through a shared MQTT broker."
	case "_mqtt._tcp", "_secure-mqtt._tcp":
		return KindMQTTBroker, "", "MQTT broker; add it under mqtt.brokers in configuration.yaml."
	case "_printer._tcp", "_ipp._tcp":
		return KindPrinter, firstNonEmpty(txt, "ty", "product"), "Printer; not supported."
	}
	return KindOther, "", "No MQTT integration known for this service type."
}
