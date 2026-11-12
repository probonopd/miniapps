# MiniHA - Specification (draft 0.1, 2026-11-05)

A resource-minimized hub for Home Assistant (HA) style devices. Devices that
announce themselves via HA MQTT discovery (Zigbee2MQTT, zwave-js-ui, Tasmota,
ESPHome in MQTT mode, OpenMQTTGateway, Valetudo, ring-mqtt, ...) show up and
can be controlled without any change on the device side. Clients that speak
the HA REST and WebSocket API see an HA-shaped entity model.

All decisions are recorded in section 14 (decided 2026-11-05).

## 1. Goals

- Zero-change compatibility with the HA MQTT ecosystem (discovery, state and
  command topics, availability, birth/last-will).
- HA-shaped data model: entities, devices, areas, services, attributes,
  entity IDs generated with HA's rules, so existing dashboards, naming habits
  and tooling carry over.
- Runs comfortably on a Raspberry Pi Zero 2 W (512 MB RAM, slow microSD) and
  on repurposed Android devices (Echo Show class). Idle RSS budget: 25 MB.
- One static binary, no runtime dependencies (no Python, no node, no DB
  server), configuration in plain files, boots in under one second.
- Linux, FreeBSD, OpenBSD, Android (as a static Linux binary).
- Everything testable headless; the HA compatibility is asserted by golden
  tests against real-world discovery payloads.

## 2. Non-goals

- Feature parity with HA (2000+ integrations, add-ons, Supervisor, Cloud).
- Running HA custom components or any Python.
- Being a Zigbee/Z-Wave stack. Those stay in Zigbee2MQTT / zwave-js-ui.
- Using the "Home Assistant" name or logo (trademark); "HA compatible" only.

## 3. Target hardware

| Device                        | CPU                         | RAM    | OS / ABI             | Notes                                      |
|-------------------------------|-----------------------------|--------|----------------------|--------------------------------------------|
| Raspberry Pi Zero 2 W         | 4x Cortex-A53 1 GHz         | 512 MB | Linux arm64 or armv7 | microSD: minimize writes                   |
| Echo Show 2nd gen (2018)      | Intel Atom x5-Z8350 4x1.44  | 2 GB   | Fire OS, x86_64      | needs root or an APK wrapper               |
| Echo Show 5 / 8 (1st gen)     | MediaTek MT8163 4x A53      | 1 GB   | Fire OS, arm64       | same, arm64                                |
| Generic SBC / old router / VM | any                         | >=256MB| Linux/FreeBSD/OpenBSD| amd64, arm64, armv7                        |

Consequences: build matrix linux/{arm64,arm,amd64,386}, freebsd/{amd64,arm64},
openbsd/{amd64,arm64}; Android is served by the static linux builds; the
storage layer must debounce and batch writes; no per-request process spawning.

## 4. Implementation language

| Criterion                                  | C        | C++      | Go (chosen)    | Rust           | Zig            |
|--------------------------------------------|----------|----------|----------------|----------------|----------------|
| Static cross-build for all targets         | manual   | manual   | trivial (no cgo)| cargo-zigbuild | trivial        |
| Idle RSS (small server)                    | 2-4 MB   | 3-6 MB   | 10-20 MB       | 3-8 MB         | 2-4 MB         |
| Binary size (stripped)                     | <1 MB    | 1-3 MB   | 8-12 MB        | 3-6 MB         | <2 MB          |
| MQTT client lib                            | mosquitto(dyn) | same | paho, mochi    | rumqttc        | none mature    |
| Embeddable MQTT broker                     | no       | no       | mochi-mqtt     | rumqttd        | no             |
| HTTP + WebSocket + TLS                     | libs, work | libs   | stdlib + 1 lib | axum/tungstenite | immature     |
| Jinja2-like templates (HA value_template)  | none     | inja     | own (see 6.8)  | minijinja (best)| none          |
| YAML, JSON, mDNS                           | libs     | libs     | all available  | all available  | partial        |
| Dev speed, review by AI agents, compile    | slow     | slow     | fast           | medium         | medium         |
| FreeBSD / OpenBSD first-class              | yes      | yes      | yes            | yes            | yes            |

Recommendation: Go.

- Meets the budget: a Go server with 300 entities measures around 20-30 MB
  RSS; on a 512 MB device that is irrelevant, and far below HA (400+ MB).
- `CGO_ENABLED=0` produces a static binary for every target in the matrix
  with one environment variable; the same binary runs on Android from a
  root shell or packaged in an APK.
- Everything needed exists as pure-Go libraries, including an embeddable
  broker, which matters on Android where there is no package manager.
- Short compile times and a small language keep the code reviewable.

Rust is the alternative if a sub-10 MB RSS target becomes a requirement or
if template fidelity matters most: minijinja is the most faithful Jinja2
implementation outside Python. Cost: slower builds, heavier async code.
C/C++ would need a hand-written or glued MQTT, HTTP, WebSocket, template and
YAML stack for a gain of ~15 MB RAM; not worth it. Zig lacks the libraries.

Libraries (Go), all pure Go:

| Need              | Choice                                   | Why                                          |
|-------------------|------------------------------------------|----------------------------------------------|
| HTTP              | net/http (stdlib)                        | no dependency                                |
| WebSocket         | github.com/coder/websocket               | small, stdlib-style, no goroutine per frame  |
| MQTT client       | github.com/eclipse/paho.golang           | MQTT 3.1.1 + 5, maintained                   |
| MQTT broker       | github.com/mochi-mqtt/server             | always compiled in; core uses its inline client |
| Templates         | internal/template (own)                  | see 6.8: pongo2 was tried and rejected       |
| YAML              | github.com/goccy/go-yaml                 | precise errors, anchors                      |
| mDNS              | github.com/grandcat/zeroconf             | browse + publish                             |
| Password hashing  | golang.org/x/crypto/argon2               |                                              |
| Config/registries | JSON files, atomic rename                | see section 8                                |
| History           | modernc.org/sqlite (pure Go)             | no cgo; write strategy in section 8          |

## 5. Architecture

Single process, single binary, goroutines per connection, one in-process
event bus. No plugins at runtime; integrations are Go packages compiled in.

```
   devices --MQTT 1883--> embedded broker (mochi-mqtt), optional bridge
          |                   to an external broker
   +------v--------+      +---------------+      +----------------+
   | mqtt          |      | template      |      | netdiscovery   |
   | discovery     |----->| (Jinja2 subset|      | (mDNS browse)  |
   | components    |      |  + HA filters)|      +-------+--------+
   +------+--------+      +---------------+              |
          | state updates / command publish               | found devices
   +------v-----------------------------------------------v--------+
   | core: entity registry, device registry, area registry,        |
   |       state machine, service registry, event bus              |
   +------+----------------+----------------+----------------------+
          |                |                |
   +------v------+  +------v------+  +------v-------+
   | automation  |  | store       |  | api          |
   | yaml engine |  | json files, |  | REST, WS,    |
   |             |  | history ring|  | static web UI|
   +-------------+  +-------------+  +--------------+
```

Event flow, inbound: MQTT message -> component parses (template) -> core
sets state -> `state_changed` event -> WebSocket subscribers, automations,
history, debounced persistence.

Event flow, outbound: service call (REST, WS, automation, UI) -> service
registry -> component renders `command_template` -> MQTT publish ->
optimistic state or wait for state topic, per HA rules.

Packages: `core`, `mqtt`, `template`, `automation`, `store`, `api`, `web`,
`netdiscovery`, `cmd/miniha`.

## 6. HA compatibility contract

This section is the acceptance criteria; everything here is tested.

### 6.1 MQTT discovery

- Prefix configurable, default `homeassistant`.
- Per-component topic `<prefix>/<component>/[<node_id>/]<object_id>/config`
  and device-based topic `<prefix>/device/[<node_id>/]<object_id>/config`
  with `cmps` (HA 2024.11+ format). Both accepted.
- Full abbreviation table (`stat_t`, `cmd_t`, `dev`, `ids`, `avty`, ...)
  and the `~` topic prefix.
- `unique_id` required for registry membership; entities without it are
  volatile (same as HA).
- `device` block feeds the device registry: identifiers, connections,
  manufacturer, model, model_id, sw_version, hw_version, name, suggested_area,
  configuration_url, via_device.
- `origin` block recorded. `availability` single topic or list with
  `availability_mode` all/any/latest, `payload_available/not_available`,
  `availability_template`.
- Retained empty payload on a config topic removes the entity (and the device
  when its last entity is gone).
- `migrate_discovery` handled for the device-based format.
- Birth / last will: publish `homeassistant/status online` at start,
  `offline` as will, topic and payloads configurable, so devices re-announce.
- Entity naming and `entity_id` generation identical to HA: `name: null`
  means entity name = device name; otherwise `<device name> <entity name>`;
  `object_id` or `default_entity_id` override the slug; collisions get `_2`.
- Common options on every component: `name`, `icon`, `device_class`,
  `state_class`, `unit_of_measurement`, `entity_category`,
  `enabled_by_default`, `json_attributes_topic`, `json_attributes_template`,
  `qos`, `retain`, `optimistic`, `encoding`, `value_template`,
  `command_template`.
- Components, P1: sensor, binary_sensor, switch, light (schema
  `default`, `json`, `template`), cover, number, select, button, text, event,
  device_tracker, update. All done 2026-11-06.
- Components, P3: climate, fan, lock, humidifier, siren, valve, vacuum,
  alarm_control_panel, scene, tag, device_automation, camera, image,
  water_heater, lawn_mower, notify. All done 2026-11-06. Known gaps per
  component are listed in the Go file headers' comments and tests;
  notable: no unit conversion to the system unit (values stay in the
  entity's unit), vacuum has no legacy schema, device_tracker computes
  no zones, water_heater away mode is not supported.
- Also non-discovery MQTT entities declared in `configuration.yaml`
  (`mqtt: sensor: - state_topic: ...`) in HA syntax.
- Tasmota native discovery (`tasmota/discovery/<mac>/config`), as HA's
  tasmota integration does: relays become switches, light relays become
  lights according to `lt_st` (unlinked RGBCW splits into an RGB light on
  POWER1 and a white light on POWER2, with Dimmer1/Dimmer2), state from
  `tele/STATE` and `stat/RESULT`, a `STATE` request on discovery and on
  LWT online, availability from `tele/LWT`. Sensors (`.../sensors`) are
  phase P1. Done 2026-11-05.
- Tasmota devices discovered through SetOption19 (HA discovery) are
  recognized by their `cmnd/.../POWERn` command topic and `LWT`
  availability; on discovery and when they come online MiniHA sends
  `STATE` and resends the device's own `TelePeriod` value, which forces
  tele/STATE and tele/SENSOR at once instead of waiting up to a
  telemetry period. Verified against Tasmota 8.1.0 and 9.1.0.

### 6.2 Entity model

- `entity_id` = `<domain>.<object_id>`, state is a string of at most 255
  characters, attributes are a JSON object, timestamps `last_changed`,
  `last_updated`, `last_reported`, plus `context` (id, user_id, parent_id).
- States `unknown` and `unavailable` with HA semantics.
- Entity registry fields: unique_id, platform, device_id, area_id, name
  override, icon override, disabled_by, hidden_by, entity_category,
  original_name, original_icon, unit override, options.
- Device registry and area registry as in HA.

### 6.3 Services

HA service names and field names, mapped to MQTT commands by the components:

- `light.turn_on` (brightness, brightness_pct, rgb_color, rgbw_color,
  rgbww_color, hs_color, xy_color, color_temp_kelvin, effect, transition,
  white), `light.turn_off`, `light.toggle`
- `switch.turn_on/turn_off/toggle`
- `cover.open_cover/close_cover/stop_cover/set_cover_position/
  open_cover_tilt/close_cover_tilt/set_cover_tilt_position`
- `number.set_value`, `select.select_option`, `button.press`,
  `text.set_value`, `update.install`
- P3: `climate.*`, `fan.*`, `lock.*`, `humidifier.*`, `siren.*`,
  `valve.*`, `vacuum.*`, `alarm_control_panel.*`
- `homeassistant.turn_on/turn_off/toggle/update_entity/reload_config_entry`
- `mqtt.publish`, `scene.turn_on`, `script.turn_on`,
  `automation.trigger/turn_on/turn_off/reload`
- Targets: `entity_id`, `device_id`, `area_id`, `all`.

### 6.4 REST API (port 8123)

Authentication: `Authorization: Bearer <long-lived token>`.

- `GET /api/`, `GET /api/config`, `GET /api/states`,
  `GET /api/states/<entity_id>`, `POST /api/states/<entity_id>`,
  `GET /api/services`, `POST /api/services/<domain>/<service>`,
  `GET /api/events`, `POST /api/events/<type>`,
  `POST /api/template`, `GET /api/error_log`,
  `GET /api/history/period/<ts>` and `GET /api/logbook/<ts>` (when history
  is enabled).

### 6.4a Event stream (MiniHA extension)

`GET /api/miniha/stream` is a server-sent events feed of `state_changed`
and `registry_updated`, used by the built-in UI so discovered and lost
devices appear without polling or restart. The HA WebSocket API (6.5)
supersedes it for third-party clients.

### 6.5 WebSocket API (`/api/websocket`)

Implemented 2026-11-06 (internal/api/ws.go): everything listed below plus
`subscribe_trigger`, `validate_config`, `supported_features` and
`auth/current_user`; HA message shapes, strictly increasing ids, a writer
goroutine per connection, compressed `subscribe_entities` diffs.

Phase 1 subset, enough for our own UI and for third-party tools that use
the HA WebSocket API:

`auth_required/auth/auth_ok/auth_invalid`, `ping/pong`, `get_states`,
`get_config`, `get_services`, `subscribe_events`, `unsubscribe_events`,
`call_service`, `subscribe_entities` (compressed state format),
`config/entity_registry/list|get|update`, `config/device_registry/list|update`,
`config/area_registry/list|create|update|delete`, `render_template`,
`subscribe_trigger` (phase 2), `validate_config` (phase 2).

Optional stretch (not planned): the auth flow used by the HA companion apps (`/auth/authorize`,
`/auth/token`, `/auth/login_flow`, refresh tokens) and the `mobile_app`
registration webhook, so the official Android/iOS apps can connect.
Further stretch: serve the official HA frontend bundle from our backend; needs
`lovelace/*`, `frontend/*`, `config_entries/*` stubs. Decided in 14.

### 6.6 Configuration files (HA syntax where HA has one)

- `configuration.yaml`: `homeassistant:` (name, latitude, longitude,
  elevation, unit_system, time_zone, currency), `mqtt:` (listen address
  and port of the embedded broker, broker users, tls, discovery,
  discovery_prefix, birth and will messages, optional `external_broker:`
  to connect to another broker instead of serving one, plus manual
  entities), `recorder:` (db path, flush interval, purge_keep_days,
  include/exclude), `http:` (server_host,
  server_port, ssl_certificate, ssl_key, cors_allowed_origins),
  `automation: !include automations.yaml`, `scene:`, `script:`.
- `automations.yaml`, `scripts.yaml`, `scenes.yaml` in HA syntax so users
  can paste existing ones. Supported subset in 6.7.
- `secrets.yaml` with `!secret` works.
- No import of an existing HA installation; devices re-announce via
  discovery, names and areas are set in MiniHA.
- Unknown keys fail startup with a clear message; no silent ignoring.

### 6.7 Automation engine (phase 2)

Triggers: `state` (to/from/for/attribute), `numeric_state`, `time`,
`time_pattern`, `mqtt`, `sun`, `template`, `event`, `homeassistant`
(start/shutdown), `webhook`.
Conditions: `state`, `numeric_state`, `time`, `template`, `sun`, `and/or/not`.
Actions: `service`/`action`, `delay`, `wait_for_trigger`, `wait_template`,
`choose`, `if/then/else`, `repeat`, `condition`, `event`, `variables`,
`parallel`, `stop`. Modes: single, restart, queued, parallel.
Scenes and scripts with the same action vocabulary.

Implemented 2026-11-05 (internal/automation): all triggers above plus
`event`; conditions state (with `for` and `match`), numeric_state, time
(with weekday and windows across midnight), sun, template, trigger,
and/or/not and the bare-template shorthand; all actions above plus
`scene`, `sequence` and `continue_on_error`; both key spellings
(`trigger`/`triggers`, `service`/`action`); `automation.<slug>`,
`script.<id>` and `scene.<slug>` entities with HA's services
(`automation.trigger/turn_on/turn_off/toggle/reload`, `script.turn_on/
turn_off/<id>/reload`, `scene.turn_on/reload`); files reload on change
and through `PUT /api/miniha/files/<name>` (validated first). Sun times
from the NOAA algorithm. Not implemented: device and zone triggers and
conditions, `for_each` over templates returning strings, trigger
`enabled` templates.

### 6.8 Templates

Own Jinja2-subset engine (internal/template) with Python value semantics.
pongo2 was evaluated first and rejected: it renders 21.5 as "21.500000",
lacks inline if, "is defined" and round, all of which real discovery
payloads use. The engine keeps the int/float distinction, Python str()
formatting, truthiness and round-half-even, and is checked by a table of
expressions with outputs taken from HA's Jinja environment. Supported:
expressions with Jinja2 precedence, filters, tests, inline if, if/elif/
else, for (with loop variable), set, slices (`s[a:b]`), dict and string
methods (items, keys, values, get, split, strip, ...), `tojson`. HA globals and filters: `value`, `value_json`,
`states`, `states(entity)`, `state_attr`, `is_state`, `is_state_attr`,
`now()`, `utcnow()`, `as_timestamp`, `as_datetime`, `timestamp_custom`,
`timestamp_local`, `float/int/round/default/abs/min/max`, `iif`,
`is_defined`, `is_number`, `to_json/from_json`, `lower/upper/title/trim/
replace/regex_*`, `|map|select|list|join|sort|unique|length`, `this`.
Golden tests hold expected outputs generated by HA's own Jinja environment.

## 7. Web UI

- Served from the binary (`embed`), plain HTML + ES modules, no build step,
  no framework, under 100 KB transferred. Talks only to the WebSocket API.
- Views: Overview (areas -> entity cards with controls), Devices, Entities,
  Automations (enable, trigger, edit YAML in place), Logbook (if history),
  Settings (MQTT, users and tokens, areas, discovery), About (resource use).
  Done 2026-11-06: Overview with controls for every domain and 24 h
  sparklines, Devices, Entities, Areas, Automations, Logbook, Settings
  (status, tokens, discovered devices), `#kiosk`.
- Mobile and kiosk first: the Echo Show screen is the primary display for
  the Android case; `/kiosk` serves a fullscreen overview without chrome.
- Optional: official HA frontend (see 6.5 stretch).

## 8. Storage and persistence

Data dir `$MINIHA_DIR`, default `/var/lib/miniha` (Android: the app's
files dir). Layout:

```
configuration.yaml  automations.yaml  scenes.yaml  scripts.yaml  secrets.yaml
registry.json        entity, device and area registries (debounced, atomic rename)
auth.json            admin password hash and token hashes
broker/              retained messages of the embedded broker (P1; P0 keeps them in RAM)
history.db           SQLite (P2), see below
```

Write minimization for config and state: debounce, batch, temp file +
rename, no fsync per change, no log files by default (stderr to
journald/syslog).

History (recorder) in SQLite, SD-card-careful (implemented 2026-11-06 in
internal/recorder, enabled by default, `recorder:` section in
configuration.yaml with `enabled`, `db_path`, `flush_interval`,
`purge_keep_days`, `include`, `exclude`):

- Pure-Go driver (modernc.org/sqlite), `journal_mode=WAL`,
  `synchronous=NORMAL`, `auto_vacuum=INCREMENTAL`, page size 4096.
- State changes are buffered in RAM and written in one transaction per
  flush interval (default 5 min, configurable, also flushed on clean
  shutdown). Power loss costs at most one interval; the live state is in
  `state.json` anyway.
- Only real changes are stored (`last_reported` updates without a state
  or attribute change are not rows). Attributes are stored once per
  distinct attribute set and referenced (HA's `state_attributes` scheme),
  entity IDs via a `states_meta` table.
- Long-term statistics: numeric sensors with `state_class` get hourly
  min/max/mean/sum rows; raw states are purged after `purge_keep_days`
  (default 10), statistics kept. Purge runs daily in small batches with
  `incremental_vacuum`, never a full `VACUUM`.
- `wal_autocheckpoint` raised so checkpoints happen about once per flush,
  not per 1000 pages.
- Include/exclude lists per domain, entity and glob, as in HA's recorder.
- Budget at 300 entities, 10 msg/s: under 20 MB written per day.

Also done 2026-11-06: `secrets.yaml` with `!secret`; manual MQTT
entities under `mqtt:` in HA syntax for every component; retained
messages of the embedded broker persisted to `broker/retained.json`
(debounced 2 s); TLS listener for the embedded broker (`mqtt.tls_listen`,
`tls_certificate`, `tls_key`) and for HTTP (`http.ssl_certificate`,
`ssl_key`); mDNS browsing (internal/netdiscovery) listing ESPHome,
Shelly, Tasmota, HomeKit, Cast, HA instances, brokers and printers with
a hint each, shown in the Settings view; service files for systemd,
OpenRC, FreeBSD and OpenBSD under deploy/; a load generator
(cmd/loadgen) and scripts/resource-test.sh as the release gate.

### 6.9 HA integrations beyond MQTT (added 2026-11-06)

All configured in `configuration.yaml` with HA's syntax and option names:

- Helpers: `input_boolean`, `input_number`, `input_select`, `input_text`,
  `input_datetime`, `input_button`, `counter`, `timer` (internal/helpers)
  with HA's services and attributes, state persisted in
  `helpers_state.json`, `<domain>.reload` services, timer events.
- `template:` sensors, binary_sensors, switches, numbers, selects and
  buttons (state, attributes, availability, icon, action lists); re-render
  on state changes (no time-based re-rendering). `group:` old style and
  `- platform: group` for light, switch, binary_sensor, sensor
  (min/max/mean/median/sum/last/range/product/count), cover and fan
  (internal/virtual).
- `zone:` (plus `zone.home` from the homeassistant section), `person:`
  (HA's tracker preference: a stationary tracker at home wins over GPS),
  `sun.sun` with elevation, azimuth and next_* attributes
  (internal/location, internal/astro). GPS MQTT trackers resolve zones.
- Automations: `webhook` (`/api/webhook/<id>`, local_only,
  allowed_methods), `tag`, `zone` and `device` (MQTT device triggers)
  triggers, `zone` condition, `time` at an input_datetime, blueprints
  (`use_blueprint`, `!input`, `blueprints/automation/`), run traces (last
  5 per automation, via REST, WebSocket and the UI), `rest_command:`,
  `persistent_notification.*` with a notification list in the UI.
- Dashboards: `ui-lovelace.yaml` in HA's Lovelace YAML with the cards
  entities, glance, markdown, button, history-graph, vertical-stack,
  horizontal-stack, conditional, gauge, picture, camera; `lovelace/config`
  over WebSocket; `/api/miniha/dashboard`.
- Export: `mqtt.export` announces this hub as one HA MQTT device on a
  chosen broker (discovery, retained state and attributes, commands back),
  the scheme from section 6.1 in reverse. Offline status is published on
  a clean stop only.
- WebSocket additions: `recorder/statistics_during_period`,
  `recorder/get_statistics_metadata`, `history/history_during_period`,
  `logbook/get_events`, `persistent_notification/get|subscribe`,
  `lovelace/config|resources`, `frontend/get_themes`,
  `config/core/check_config`, `system_health/info`, `recorder/info`.
- CLI: `miniha backup FILE.tar.gz`, `restore FILE.tar.gz [--force]`,
  `check-config`.

Still not implemented: companion app login flow, official HA frontend,
device conditions and actions, trigger-based template entities, template
lights, time-based template re-rendering, multi-user.

## 9. Security

- Listens on `0.0.0.0:8123` by default (HA's port; apps expect it),
  optional TLS from cert files, optional reverse proxy headers.
- One admin user with an argon2id-hashed password; UI login sets an HttpOnly cookie;
  long-lived tokens stored hashed; every API call authenticated.
- MQTT: username/password via `mqtt.users`; without it the broker accepts
  anonymous clients and logs a warning at startup. TLS listener in P1.
- Runs as an unprivileged user; no shelling out; config dir is the only
  writable path.

## 10. Resource budget (release gate)

Measured 2026-11-06 on an x86-64 host with `scripts/resource-test.sh`
(300 entities, 10 msg/s, recorder on): binary 16.4 MB, idle RSS 22.8 MB,
max RSS 28.7 MB, CPU 0.6 % of one core, 336 KB written in 36 s. The binary
and idle targets below are missed by the pure-Go SQLite driver (about
3 MB of code and 5 MB of RSS); a build without the recorder would meet
them. Accepted for now; revisit with a build tag if the Pi Zero 2 needs it.

| Metric                               | Target (Go)     |
|--------------------------------------|-----------------|
| Binary, stripped                     | <= 12 MB        |
| Idle RSS, 0 entities                 | <= 15 MB        |
| RSS, 300 entities, 10 msg/s          | <= 40 MB        |
| CPU, 300 entities, 10 msg/s, Pi Zero 2 | <= 3 % of one core |
| Startup to API ready                 | <= 1 s          |
| Writes at idle, recorder on          | <= 2 MB / day   |
| Writes, 300 entities, 10 msg/s       | <= 20 MB / day  |

Measured by a test harness that publishes synthetic discovery for 300
entities and a message stream, run on a Pi Zero 2 W and in qemu-system-aarch64
in CI. Exceeding a target fails the release build.

## 11. Build and deployment

- Go 1.23+, `CGO_ENABLED=0 go build -trimpath -ldflags="-s -w"`, one
  `make <os>-<arch>` per matrix entry, reproducible.
- Linux: systemd unit and OpenRC script; FreeBSD and OpenBSD: rc.d scripts.
  All run as user `miniha`.
- Android / Fire OS:
  - rooted: push the static binary to `/data/local/miniha`, start from an
    init script or Termux.
  - APK wrapper (preferred for a sealed device): the binary ships as
    `lib/<abi>/libminiha.so` (x86_64 for Echo Show 2, arm64-v8a for the
    MediaTek models), started as a foreground service, with a WebView in
    kiosk mode showing `http://127.0.0.1:8123/kiosk`. Built without the
    Android SDK (javac + d8 + apktool, as done for FMRack for Android).
- MQTT broker: always embedded (mochi-mqtt), listening on 1883 and
  optionally 8883 with TLS; no mosquitto needed. An existing broker can be
  used instead via `mqtt.external_broker` (paho.golang client with
  automatic reconnect, subscriptions replayed and the birth message resent
  on every reconnect, last will registered with the broker). Done
  2026-11-05.
- Several brokers at once via `mqtt.brokers` (list), optionally plus the
  embedded one (`embedded_broker`). Each broker has its own discovery
  and connection; named brokers register their entities and devices
  under platform `mqtt/<name>` so identical unique_ids from different
  sites stay apart (HA has no equivalent, it knows one broker). An
  unreachable broker does not block startup; its connection retries in
  the background and subscriptions plus birth are replayed when it comes
  up. Done 2026-11-05.

## 12. Phases

| Phase | Deliverable                                                                                     | Done when                                                                 |
|-------|-------------------------------------------------------------------------------------------------|---------------------------------------------------------------------------|
| P0    | core, MQTT discovery for sensor/binary_sensor/switch/light, REST, token auth, minimal UI (done 2026-11-05) | Zigbee2MQTT devices appear; a switch toggles from the UI and via REST |
| P1    | all phase-1 components, templates, availability, registries, persistence, WS API, full UI        | HA WebSocket tools (e.g. hass-cli) work; resource budget met              |
| P2    | automations/scenes/scripts (done 2026-11-05), SQLite history, logbook, mDNS device listing (done 2026-11-06) | existing automations.yaml of the stated subset runs unchanged |
| P3    | phase-2 components (climate, fan, lock, ...) (done 2026-11-06)                                  | golden corpus for those components passes                                 |
| later | optional, unplanned: companion app auth flow, official HA frontend, native ESPHome/Shelly        | evaluated per item                                                        |

## 13. Testing

- Golden corpus of real discovery payloads (Zigbee2MQTT, zwave-js-ui,
  Tasmota, ESPHome, OpenMQTTGateway, Valetudo, ring-mqtt) -> expected
  entity IDs, states, attributes, devices. Test-first for every component.
- Template conformance table with expected outputs from HA's Jinja.
- Integration tests against mosquitto (CI container) with a fake device.
- REST and WS conformance tests derived from the HA developer docs.
- Resource tests from section 10.
- Standard Go `testing`; `go vet` and `staticcheck` clean; no warnings.

## 14. Decisions

Decided 2026-11-05:

1. Language: Go.
2. Compatibility level: device side + REST + WS subset (P0-P2). Companion
   app support and the official frontend stay optional stretch items.
3. MQTT broker always embedded.
4. History: SQLite, written SD-card-carefully (section 8).

5. Automations: HA YAML subset (section 6.7), phase 2.
6. Users: single admin plus long-lived tokens.
7. Non-MQTT devices: mDNS listing with hints only; no native protocols in v1.
8. License MIT (whole miniapps repository). Name MiniHA.
9. No import from an existing HA installation.
