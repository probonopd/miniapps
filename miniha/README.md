# MiniHA

A resource-minimized hub for Home Assistant style devices: embedded MQTT
broker, HA MQTT discovery, HA-shaped entities and services, HA-compatible
REST API and a small web UI, in one static binary. See SPEC.md for the
design and the compatibility contract.

Status: phases P0 to P3 of SPEC.md plus the HA integrations of section
6.9 (helpers, template and group entities, zones, persons, sun,
blueprints, traces, webhooks, rest_command, notifications, Lovelace YAML
dashboards, MQTT export, backup/restore) are implemented: every HA MQTT
component (sensor, binary_sensor, switch, light with all three schemas,
cover, number, select, button, text, event, device_tracker, update,
climate, fan, humidifier, water_heater, lock, siren, valve,
alarm_control_panel, lawn_mower, vacuum, tag, device_automation, scene,
notify, camera, image), Tasmota native discovery, manual MQTT entities,
embedded plus any number of external brokers (TLS, retained messages
persisted), HA REST and WebSocket APIs, automations, scripts and scenes,
SQLite history with logbook, mDNS device listing, a web UI with
Overview, Devices, Entities, Areas, Automations, Logbook, Settings and a
kiosk mode, and service files for systemd, OpenRC, FreeBSD and OpenBSD
(see deploy/README.md).

## Not implemented

The HA companion app login flow, the official HA frontend, device
conditions and actions, trigger-based template entities, and the Android
APK wrapper (SPEC section 11) are not part of this tree.

## Dashboards and helpers

`ui-lovelace.yaml` (HA Lovelace YAML: entities, glance, markdown,
button, history-graph, stacks, conditional, gauge, picture, camera cards)
is rendered as the default view; helpers (`input_boolean`, `input_number`,
`input_select`, `input_text`, `input_datetime`, `input_button`, `counter`,
`timer`), `template:`, `group:`, `zone:`, `person:` and `rest_command:`
go into configuration.yaml exactly as in HA. `miniha check-config`
validates everything, `miniha backup out.tar.gz` saves the data dir.

## Build

    make            # ./miniha for this machine
    make all        # dist/ for linux (arm64, arm, amd64, 386), freebsd, openbsd
    make test

## Run

    ./miniha -data ./data set-password      # optional; without it the UI and API need no login
    ./miniha -data ./data token create cli  # long-lived API token, printed once
    ./miniha -data ./data run

The first run writes `configuration.yaml` into the data directory. Point
Zigbee2MQTT, Tasmota, ESPHome (MQTT) or any other HA discovery capable
device at port 1883 of this machine, then open http://<host>:8123/.

    curl -H "Authorization: Bearer $TOKEN" http://localhost:8123/api/states

## Automations

`automations.yaml`, `scripts.yaml` and `scenes.yaml` in the data directory
use Home Assistant's syntax and reload on save (also from the Automations
view in the web UI, which validates before writing):

    - alias: Dim bulb when the projector shows a picture
      trigger:
        - platform: state
          entity_id: binary_sensor.projector_tv_picture
          to: "on"
      action:
        - service: light.turn_on
          target: { entity_id: light.bulb }
          data: { brightness_pct: 20 }

Triggers: state, numeric_state, time, time_pattern, mqtt, sun, template,
event, homeassistant. Conditions: state, numeric_state, time, sun,
template, trigger, and/or/not. Actions: service calls, delay,
wait_template, wait_for_trigger, choose, if, repeat, condition, event,
variables, stop, parallel, scene. Modes single, restart, queued, parallel.

## Using an existing broker

Add to `configuration.yaml` to make MiniHA a client of another broker
instead of serving its own (the embedded broker is then disabled):

    mqtt:
      external_broker:
        host: 192.0.2.10
        port: 1883

Several brokers at once, each in its own entity namespace, with the
embedded broker still serving local devices:

    mqtt:
      brokers:
        - name: public
          host: broker.example.com
          port: 8883
          tls: true
          username: me
          password: secret

Tasmota devices are picked up through their native discovery
(`tasmota/discovery/<mac>/config`) as well, so `SetOption19` is not needed.
