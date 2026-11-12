/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

// Package store owns everything on disk: configuration.yaml, the auth
// file and the persisted registries.
package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/goccy/go-yaml"
)

// Config is configuration.yaml. Keys follow HA where HA has them, so a
// user can carry over the homeassistant: and http: sections unchanged.
type Config struct {
	Homeassistant HAConfig       `yaml:"homeassistant"`
	MQTT          MQTTConfig     `yaml:"mqtt"`
	HTTP          HTTPConfig     `yaml:"http"`
	Recorder      RecorderConfig `yaml:"recorder"`

	// HA integration sections kept as raw YAML; each package validates
	// its own (helpers, template, group, zones, persons, rest_command).
	InputBoolean  map[string]any   `yaml:"input_boolean"`
	InputNumber   map[string]any   `yaml:"input_number"`
	InputSelect   map[string]any   `yaml:"input_select"`
	InputText     map[string]any   `yaml:"input_text"`
	InputDatetime map[string]any   `yaml:"input_datetime"`
	InputButton   map[string]any   `yaml:"input_button"`
	Counter       map[string]any   `yaml:"counter"`
	Timer         map[string]any   `yaml:"timer"`
	Template      []map[string]any `yaml:"template"`
	Group         map[string]any   `yaml:"group"`
	Light         []map[string]any `yaml:"light"`
	Switch        []map[string]any `yaml:"switch"`
	BinarySensor  []map[string]any `yaml:"binary_sensor"`
	Sensor        []map[string]any `yaml:"sensor"`
	Cover         []map[string]any `yaml:"cover"`
	Fan           []map[string]any `yaml:"fan"`
	RestCommand   map[string]any   `yaml:"rest_command"`
	Zone          []map[string]any `yaml:"zone"`
	Person        []map[string]any `yaml:"person"`
}

// RecorderConfig is the recorder: section (HA names). The database lives
// in the data directory unless db_path is set.
type RecorderConfig struct {
	Enabled       *bool    `yaml:"enabled"`
	DBPath        string   `yaml:"db_path"`
	FlushInterval string   `yaml:"flush_interval"` // Go duration such as "5m"
	PurgeKeepDays int      `yaml:"purge_keep_days"`
	Include       []string `yaml:"include"`
	Exclude       []string `yaml:"exclude"`
	flush         time.Duration
}

func (r *RecorderConfig) IsEnabled() bool      { return r.Enabled == nil || *r.Enabled }
func (r *RecorderConfig) Flush() time.Duration { return r.flush }

type HAConfig struct {
	Name       string  `yaml:"name"`
	Latitude   float64 `yaml:"latitude"`
	Longitude  float64 `yaml:"longitude"`
	Elevation  float64 `yaml:"elevation"`
	UnitSystem string  `yaml:"unit_system"`
	TimeZone   string  `yaml:"time_zone"`
	Currency   string  `yaml:"currency"`
}

type MQTTConfig struct {
	Listen           string                `yaml:"listen"`
	DiscoveryPrefix  string                `yaml:"discovery_prefix"`
	Users            map[string]string     `yaml:"users"`
	BirthMessage     *MessageConfig        `yaml:"birth_message"`
	WillMessage      *MessageConfig        `yaml:"will_message"`
	TasmotaDiscovery *bool                 `yaml:"tasmota_discovery"`
	EmbeddedBroker   *bool                 `yaml:"embedded_broker"`
	ExternalBroker   *ExternalBrokerConfig `yaml:"external_broker"`
	Brokers          []BrokerConfig        `yaml:"brokers"`
	Export           *ExportConfig         `yaml:"export"`

	// TLS listener of the embedded broker; all three or none.
	TLSListen      string `yaml:"tls_listen"`
	TLSCertificate string `yaml:"tls_certificate"`
	TLSKey         string `yaml:"tls_key"`

	// Manual entities, HA syntax: one list per component. They stay
	// untyped maps because the options are those of MQTT discovery
	// payloads and are validated by the entity constructors.
	Sensor            []map[string]any `yaml:"sensor"`
	BinarySensor      []map[string]any `yaml:"binary_sensor"`
	Switch            []map[string]any `yaml:"switch"`
	Light             []map[string]any `yaml:"light"`
	Cover             []map[string]any `yaml:"cover"`
	Number            []map[string]any `yaml:"number"`
	Select            []map[string]any `yaml:"select"`
	Button            []map[string]any `yaml:"button"`
	Text              []map[string]any `yaml:"text"`
	Fan               []map[string]any `yaml:"fan"`
	Climate           []map[string]any `yaml:"climate"`
	Lock              []map[string]any `yaml:"lock"`
	Event             []map[string]any `yaml:"event"`
	DeviceTracker     []map[string]any `yaml:"device_tracker"`
	Update            []map[string]any `yaml:"update"`
	Humidifier        []map[string]any `yaml:"humidifier"`
	Siren             []map[string]any `yaml:"siren"`
	Valve             []map[string]any `yaml:"valve"`
	Vacuum            []map[string]any `yaml:"vacuum"`
	AlarmControlPanel []map[string]any `yaml:"alarm_control_panel"`
	Scene             []map[string]any `yaml:"scene"`
	Notify            []map[string]any `yaml:"notify"`
	Camera            []map[string]any `yaml:"camera"`
	Image             []map[string]any `yaml:"image"`
	LawnMower         []map[string]any `yaml:"lawn_mower"`
	WaterHeater       []map[string]any `yaml:"water_heater"`
	Tag               []map[string]any `yaml:"tag"`
	DeviceAutomation  []map[string]any `yaml:"device_automation"`
}

// ManualEntity is one entity declared under mqtt: in configuration.yaml.
type ManualEntity struct {
	Component string
	Config    map[string]any
}

// ManualEntities lists the configured manual entities in a stable order
// (component order of the struct, then list order) so that their
// discovery ids are the same on every start.
func (c *MQTTConfig) ManualEntities() []ManualEntity {
	lists := []struct {
		component string
		items     []map[string]any
	}{
		{"sensor", c.Sensor}, {"binary_sensor", c.BinarySensor}, {"switch", c.Switch},
		{"light", c.Light}, {"cover", c.Cover}, {"number", c.Number}, {"select", c.Select},
		{"button", c.Button}, {"text", c.Text}, {"fan", c.Fan}, {"climate", c.Climate},
		{"lock", c.Lock}, {"event", c.Event}, {"device_tracker", c.DeviceTracker},
		{"update", c.Update}, {"humidifier", c.Humidifier}, {"siren", c.Siren},
		{"valve", c.Valve}, {"vacuum", c.Vacuum}, {"alarm_control_panel", c.AlarmControlPanel},
		{"scene", c.Scene}, {"notify", c.Notify}, {"camera", c.Camera}, {"image", c.Image},
		{"lawn_mower", c.LawnMower}, {"water_heater", c.WaterHeater}, {"tag", c.Tag},
		{"device_automation", c.DeviceAutomation},
	}
	var out []ManualEntity
	for _, l := range lists {
		for _, it := range l.items {
			out = append(out, ManualEntity{Component: l.component, Config: it})
		}
	}
	return out
}

// ExternalBrokerConfig is the single-broker shorthand: one external
// broker instead of the embedded one.
type ExternalBrokerConfig struct {
	Host             string `yaml:"host"`
	Port             int    `yaml:"port"`
	Username         string `yaml:"username"`
	Password         string `yaml:"password"`
	TLS              bool   `yaml:"tls"`
	ClientID         string `yaml:"client_id"`
	MaxSubscriptions int    `yaml:"max_subscriptions"`
}

// ExportConfig announces this hub's entities as one MQTT device on a
// broker (HA discovery), so another HA or MiniHA can control it.
type ExportConfig struct {
	Broker          string   `yaml:"broker"`           // name from mqtt.brokers, "external", or "embedded"
	Namespace       string   `yaml:"namespace"`        // topic prefix, default "miniha/<hub id>"
	DiscoveryPrefix string   `yaml:"discovery_prefix"` // default homeassistant
	Name            string   `yaml:"name"`             // device name, default the location name
	Include         []string `yaml:"include"`          // entity ids, domains or globs; empty = all
	Exclude         []string `yaml:"exclude"`
	CommandMaxAge   string   `yaml:"command_max_age"` // Go duration, default 30s
}

// BrokerConfig is one entry of mqtt.brokers: an additional broker whose
// entities live in their own namespace (platform "mqtt/<name>").
type BrokerConfig struct {
	Name             string `yaml:"name"`
	Host             string `yaml:"host"`
	Port             int    `yaml:"port"`
	Username         string `yaml:"username"`
	Password         string `yaml:"password"`
	TLS              bool   `yaml:"tls"`
	ClientID         string `yaml:"client_id"`
	MaxSubscriptions int    `yaml:"max_subscriptions"` // 0 = exact filters; hosted brokers often cap at 10
	DiscoveryPrefix  string `yaml:"discovery_prefix"`
	TasmotaDiscovery *bool  `yaml:"tasmota_discovery"`
}

func (c *MQTTConfig) TasmotaEnabled() bool {
	return c.TasmotaDiscovery == nil || *c.TasmotaDiscovery
}

// EmbeddedEnabled: on by default, off when only the legacy
// external_broker shorthand is used, always settable explicitly.
func (c *MQTTConfig) EmbeddedEnabled() bool {
	if c.EmbeddedBroker != nil {
		return *c.EmbeddedBroker
	}
	return c.ExternalBroker == nil
}

// ExternalBrokers returns every external broker to connect to, the
// legacy shorthand first with an empty name (namespace "mqtt").
func (c *MQTTConfig) ExternalBrokers() []BrokerConfig {
	var out []BrokerConfig
	if eb := c.ExternalBroker; eb != nil {
		out = append(out, BrokerConfig{Host: eb.Host, Port: eb.Port, Username: eb.Username, Password: eb.Password, TLS: eb.TLS, ClientID: eb.ClientID, MaxSubscriptions: eb.MaxSubscriptions})
	}
	for _, b := range c.Brokers {
		if b.DiscoveryPrefix == "" {
			b.DiscoveryPrefix = c.DiscoveryPrefix
		}
		if b.TasmotaDiscovery == nil {
			b.TasmotaDiscovery = c.TasmotaDiscovery
		}
		out = append(out, b)
	}
	for i := range out {
		if out[i].DiscoveryPrefix == "" {
			out[i].DiscoveryPrefix = c.DiscoveryPrefix
		}
	}
	return out
}

var brokerName = regexp.MustCompile(`^[a-z0-9_]+$`)

type MessageConfig struct {
	Topic   string `yaml:"topic"`
	Payload string `yaml:"payload"`
	QoS     int    `yaml:"qos"`
	Retain  bool   `yaml:"retain"`
}

type HTTPConfig struct {
	ServerHost string `yaml:"server_host"`
	ServerPort int    `yaml:"server_port"`

	// Both or neither; paths are relative to the configuration directory.
	SSLCertificate string `yaml:"ssl_certificate"`
	SSLKey         string `yaml:"ssl_key"`
}

const DefaultConfig = `# MiniHA configuration. Keys follow Home Assistant where it has them.
homeassistant:
  name: Home
  unit_system: metric
  # time_zone: Europe/Berlin

mqtt:
  listen: ":1883"
  discovery_prefix: homeassistant
  # Require credentials from MQTT clients (recommended):
  # users:
  #   zigbee2mqtt: change-me
  # Use an existing broker instead of the embedded one:
  # external_broker:
  #   host: 192.0.2.10
  #   port: 1883
  #   username: ""
  #   password: ""
  # Additional brokers (each gets its own entity namespace mqtt/<name>);
  # set embedded_broker: false to run without the embedded one:
  # brokers:
  #   - name: public
  #     host: broker.example.com
  #     port: 8883
  #     tls: true
  #     username: ""
  #     password: ""
  #     discovery_prefix: homeassistant
  #     max_subscriptions: 10   # hosted brokers often cap subscriptions per connection
  # tasmota_discovery: true   # also read tasmota/discovery/+/config
  # TLS listener of the embedded broker (all three or none; relative
  # paths are relative to this directory):
  # tls_listen: ":8883"
  # tls_certificate: broker.crt
  # tls_key: broker.key
  # Manual entities, same options as MQTT discovery payloads:
  # sensor:
  #   - name: Outdoor temperature
  #     state_topic: garden/temperature
  #     unit_of_measurement: "C"
  #     value_template: "{{ value_json.temp }}"

http:
  server_host: 0.0.0.0
  server_port: 8123

# State history in SQLite, written in batches (default every 5 minutes)
# to spare the SD card. Raw states are kept purge_keep_days, hourly
# statistics forever.
recorder:
  enabled: true
  # flush_interval: 5m
  # purge_keep_days: 10
  # exclude: [sensor.sonoff_status]
  # HTTPS (both or neither; relative paths are relative to this directory):
  # ssl_certificate: server.crt
  # ssl_key: server.key
`

// LoadConfig reads and validates configuration.yaml. Unknown keys are an
// error so that typos never pass silently.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	if data, err = substituteSecrets(data, dir); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	cfg := &Config{}
	if err := yaml.UnmarshalWithOptions(data, cfg, yaml.Strict()); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	if err := cfg.applyDefaults(dir); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return cfg, nil
}

func (c *Config) applyDefaults(dir string) error {
	if c.Homeassistant.Name == "" {
		c.Homeassistant.Name = "Home"
	}
	switch c.Homeassistant.UnitSystem {
	case "":
		c.Homeassistant.UnitSystem = "metric"
	case "metric", "us_customary", "imperial":
	default:
		return fmt.Errorf("homeassistant.unit_system must be metric or us_customary")
	}
	if c.Homeassistant.TimeZone == "" {
		c.Homeassistant.TimeZone = time.Local.String()
	}
	if _, err := time.LoadLocation(c.Homeassistant.TimeZone); err != nil {
		return fmt.Errorf("homeassistant.time_zone: %w", err)
	}
	if c.MQTT.Listen == "" {
		c.MQTT.Listen = ":1883"
	}
	if c.MQTT.DiscoveryPrefix == "" {
		c.MQTT.DiscoveryPrefix = "homeassistant"
	}
	if c.MQTT.BirthMessage == nil {
		c.MQTT.BirthMessage = &MessageConfig{Topic: "homeassistant/status", Payload: "online"}
	}
	if c.MQTT.WillMessage == nil {
		c.MQTT.WillMessage = &MessageConfig{Topic: "homeassistant/status", Payload: "offline"}
	}
	for _, m := range []*MessageConfig{c.MQTT.BirthMessage, c.MQTT.WillMessage} {
		if m.Topic == "" {
			return errors.New("mqtt birth/will message needs a topic")
		}
		if m.QoS < 0 || m.QoS > 2 {
			return errors.New("mqtt birth/will qos must be 0, 1 or 2")
		}
	}
	if eb := c.MQTT.ExternalBroker; eb != nil {
		if eb.Host == "" {
			return errors.New("mqtt.external_broker.host must be set")
		}
		if eb.Port < 0 || eb.Port > 65535 {
			return errors.New("mqtt.external_broker.port is out of range")
		}
	}
	names := map[string]bool{}
	for i, b := range c.MQTT.Brokers {
		if !brokerName.MatchString(b.Name) {
			return fmt.Errorf("mqtt.brokers[%d].name must be a lowercase identifier (a-z, 0-9, _)", i)
		}
		if names[b.Name] {
			return fmt.Errorf("mqtt.brokers: name %q used twice", b.Name)
		}
		names[b.Name] = true
		if b.Host == "" {
			return fmt.Errorf("mqtt.brokers[%d] (%s): host must be set", i, b.Name)
		}
		if b.Port < 0 || b.Port > 65535 {
			return fmt.Errorf("mqtt.brokers[%d] (%s): port is out of range", i, b.Name)
		}
	}
	if !c.MQTT.EmbeddedEnabled() && c.MQTT.ExternalBroker == nil && len(c.MQTT.Brokers) == 0 {
		return errors.New("mqtt: embedded_broker is off and no brokers are configured")
	}
	if err := c.checkTLS(dir); err != nil {
		return err
	}
	if c.Recorder.FlushInterval != "" {
		d, err := time.ParseDuration(c.Recorder.FlushInterval)
		if err != nil || d <= 0 {
			return fmt.Errorf("recorder.flush_interval: must be a positive duration such as 5m")
		}
		c.Recorder.flush = d
	}
	if c.Recorder.PurgeKeepDays < 0 {
		return errors.New("recorder.purge_keep_days must not be negative")
	}
	if c.HTTP.ServerHost == "" {
		c.HTTP.ServerHost = "0.0.0.0"
	}
	if c.HTTP.ServerPort == 0 {
		c.HTTP.ServerPort = 8123
	}
	return nil
}

// checkTLS validates the certificate options and resolves their paths
// against the configuration directory. A listener that silently falls
// back to plain text would defeat the point of asking for TLS, so a
// missing file is a startup error.
func (c *Config) checkTLS(dir string) error {
	if (c.HTTP.SSLCertificate == "") != (c.HTTP.SSLKey == "") {
		return errors.New("http.ssl_certificate and http.ssl_key must be set together")
	}
	if c.HTTP.SSLCertificate != "" {
		var err error
		if c.HTTP.SSLCertificate, err = existingFile(dir, "http.ssl_certificate", c.HTTP.SSLCertificate); err != nil {
			return err
		}
		if c.HTTP.SSLKey, err = existingFile(dir, "http.ssl_key", c.HTTP.SSLKey); err != nil {
			return err
		}
	}
	m := &c.MQTT
	set := 0
	for _, v := range []string{m.TLSListen, m.TLSCertificate, m.TLSKey} {
		if v != "" {
			set++
		}
	}
	if set == 0 {
		return nil
	}
	if set != 3 {
		return errors.New("mqtt.tls_listen, mqtt.tls_certificate and mqtt.tls_key must be set together")
	}
	if !c.MQTT.EmbeddedEnabled() {
		return errors.New("mqtt.tls_listen needs the embedded broker")
	}
	var err error
	if m.TLSCertificate, err = existingFile(dir, "mqtt.tls_certificate", m.TLSCertificate); err != nil {
		return err
	}
	m.TLSKey, err = existingFile(dir, "mqtt.tls_key", m.TLSKey)
	return err
}

func existingFile(dir, option, path string) (string, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	st, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("%s: %w", option, err)
	}
	if st.IsDir() {
		return "", fmt.Errorf("%s: %s is a directory", option, path)
	}
	return path, nil
}

// WriteAtomic writes a file via temp file and rename so that a crash
// never leaves a half-written file behind.
func WriteAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
