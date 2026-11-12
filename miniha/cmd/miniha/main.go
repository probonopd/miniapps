/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

// Command miniha is the hub: embedded MQTT broker, HA discovery, REST
// API and web UI in one process.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/probonopd/miniha/internal/api"
	"github.com/probonopd/miniha/internal/automation"
	"github.com/probonopd/miniha/internal/core"
	"github.com/probonopd/miniha/internal/helpers"
	"github.com/probonopd/miniha/internal/location"
	"github.com/probonopd/miniha/internal/mqtt"
	"github.com/probonopd/miniha/internal/netdiscovery"
	"github.com/probonopd/miniha/internal/recorder"
	"github.com/probonopd/miniha/internal/restcmd"
	"github.com/probonopd/miniha/internal/store"
	"github.com/probonopd/miniha/internal/virtual"
	"github.com/probonopd/miniha/web"
	"golang.org/x/term"
)

const version = "0.1.0"

func usage() {
	fmt.Fprintf(os.Stderr, `Usage: miniha [-data DIR] <command>

Commands:
  run                  start the hub (default)
  set-password         set the admin password for the web UI
  token create NAME    create a long-lived API token (printed once)
  token list           list API tokens
  token delete ID      delete an API token
  backup FILE          write a .tar.gz of the data directory
  restore FILE         restore a backup into the data directory (stop miniha first)
  check-config         validate configuration.yaml and the automation files
  version              print the version

The data directory holds configuration.yaml, auth.json and registry.json.
It defaults to $MINIHA_DIR, then /var/lib/miniha.
`)
}

func main() {
	defaultDir := os.Getenv("MINIHA_DIR")
	if defaultDir == "" {
		defaultDir = "/var/lib/miniha"
	}
	dataDir := flag.String("data", defaultDir, "data directory")
	flag.Usage = usage
	flag.Parse()
	args := flag.Args()
	cmd := "run"
	if len(args) > 0 {
		cmd = args[0]
		args = args[1:]
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	var err error
	switch cmd {
	case "run":
		err = run(*dataDir, log)
	case "set-password":
		err = setPassword(*dataDir)
	case "token":
		err = tokenCmd(*dataDir, args)
	case "version":
		fmt.Println("miniha " + version)
	case "backup", "restore", "check-config":
		err = extraCommand(cmd, *dataDir, args)
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func setPassword(dir string) error {
	a, err := store.LoadAuth(filepath.Join(dir, "auth.json"))
	if err != nil {
		return err
	}
	fmt.Fprint(os.Stderr, "New admin password: ")
	var pw string
	if term.IsTerminal(int(os.Stdin.Fd())) {
		raw, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return err
		}
		pw = string(raw)
	} else {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return err
		}
		pw = strings.TrimRight(line, "\r\n")
	}
	if err := a.SetPassword(pw); err != nil {
		return err
	}
	if pw == "" {
		fmt.Fprintln(os.Stderr, "password cleared; the web UI and API are open without login")
	} else {
		fmt.Fprintln(os.Stderr, "password set")
	}
	return nil
}

func tokenCmd(dir string, args []string) error {
	a, err := store.LoadAuth(filepath.Join(dir, "auth.json"))
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return errors.New("token needs a subcommand: create NAME, list, delete ID")
	}
	switch args[0] {
	case "create":
		if len(args) != 2 {
			return errors.New("token create needs a name")
		}
		plain, t, err := a.CreateToken(args[1])
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "token %s created; it is shown only once:\n", t.ID)
		fmt.Println(plain)
	case "list":
		for _, t := range a.ListTokens() {
			fmt.Printf("%s\t%s\t%s\n", t.ID, t.CreatedAt.Format(time.RFC3339), t.Name)
		}
	case "delete":
		if len(args) != 2 {
			return errors.New("token delete needs an id")
		}
		return a.DeleteToken(args[1])
	default:
		return fmt.Errorf("unknown token subcommand %q", args[0])
	}
	return nil
}

func run(dir string, log *slog.Logger) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	cfgPath := filepath.Join(dir, "configuration.yaml")
	if _, err := os.Stat(cfgPath); errors.Is(err, os.ErrNotExist) {
		if err := store.WriteAtomic(cfgPath, []byte(store.DefaultConfig), 0o644); err != nil {
			return err
		}
		log.Info("wrote default configuration", "path", cfgPath)
	}
	cfg, err := store.LoadConfig(cfgPath)
	if err != nil {
		return err
	}
	auth, err := store.LoadAuth(filepath.Join(dir, "auth.json"))
	if err != nil {
		return err
	}
	if !auth.HasPassword() {
		log.Warn("no admin password set: web UI and API are open to everyone on the network; run 'miniha set-password' to require a login")
	}

	c := core.New()
	registry := store.NewRegistrySaver(filepath.Join(dir, "registry.json"), c, 5*time.Second, log)
	if err := registry.Load(); err != nil {
		return err
	}
	c.OnRegistryChange(registry.Trigger)

	loc, err := time.LoadLocation(cfg.Homeassistant.TimeZone)
	if err != nil {
		return err
	}
	// Zones, persons and the sun entity come before devices so GPS
	// trackers can resolve zones from their first message.
	zones, err := location.NewZones(c, location.Home{Latitude: cfg.Homeassistant.Latitude, Longitude: cfg.Homeassistant.Longitude, Name: cfg.Homeassistant.Name}, cfg.Zone)
	if err != nil {
		return fmt.Errorf("zone: %w", err)
	}
	persons, err := location.NewPersons(c, cfg.Person)
	if err != nil {
		return fmt.Errorf("person: %w", err)
	}
	var sun *location.Sun
	if cfg.Homeassistant.Latitude != 0 || cfg.Homeassistant.Longitude != 0 {
		sun = location.NewSun(c, cfg.Homeassistant.Latitude, cfg.Homeassistant.Longitude, loc)
	} else {
		log.Info("sun.sun not created: set homeassistant.latitude and longitude in configuration.yaml")
	}

	birth := &mqtt.Will{Topic: cfg.MQTT.BirthMessage.Topic, Payload: cfg.MQTT.BirthMessage.Payload, QoS: byte(cfg.MQTT.BirthMessage.QoS), Retain: cfg.MQTT.BirthMessage.Retain}
	will := &mqtt.Will{Topic: cfg.MQTT.WillMessage.Topic, Payload: cfg.MQTT.WillMessage.Payload, QoS: byte(cfg.MQTT.WillMessage.QoS), Retain: cfg.MQTT.WillMessage.Retain}

	// Every broker, embedded or external, gets its own discovery feeding
	// the one registry. Named external brokers live in their own
	// namespace so that identical unique_ids from two sites never merge.
	type link struct {
		name      string
		disc      *mqtt.Discovery
		transport mqtt.Transport
		stop      func() error
		status    func() any
	}
	var links []link
	if cfg.MQTT.EmbeddedEnabled() {
		broker, err := mqtt.NewBroker(mqtt.BrokerConfig{Listen: cfg.MQTT.Listen, Users: cfg.MQTT.Users, TLSListen: cfg.MQTT.TLSListen, TLSCertificate: cfg.MQTT.TLSCertificate, TLSKey: cfg.MQTT.TLSKey, DataDir: dir}, log)
		if err != nil {
			return err
		}
		if err := broker.Start(); err != nil {
			return fmt.Errorf("mqtt broker: %w", err)
		}
		log.Info("mqtt broker listening", "addr", cfg.MQTT.Listen)
		disc := mqtt.NewDiscovery(c, broker, mqtt.Options{Prefix: cfg.MQTT.DiscoveryPrefix, Birth: birth, Will: will, Tasmota: cfg.MQTT.TasmotaEnabled()}, log.With("broker", "embedded"))
		links = append(links, link{"embedded", disc, broker, broker.Stop, func() any { return map[string]any{"clients": broker.ClientCount()} }})
	}
	for _, b := range cfg.MQTT.ExternalBrokers() {
		name := b.Name
		platform := "mqtt"
		if name != "" {
			platform = "mqtt/" + name
		} else {
			name = "external"
		}
		client, err := mqtt.NewClient(mqtt.ClientConfig{Host: b.Host, Port: b.Port, Username: b.Username, Password: b.Password, TLS: b.TLS, ClientID: b.ClientID, Will: will, MaxSubscriptions: b.MaxSubscriptions}, log.With("broker", name))
		if err != nil {
			return err
		}
		disc := mqtt.NewDiscovery(c, client, mqtt.Options{Prefix: b.DiscoveryPrefix, Birth: birth, Will: will, Tasmota: b.TasmotaDiscovery == nil || *b.TasmotaDiscovery, Platform: platform}, log.With("broker", name))
		client.SetOnConnect(func() {
			if err := disc.PublishBirth(); err != nil {
				log.Warn("birth message failed", "broker", name, "err", err)
			}
		})
		if err := client.Start(); err != nil {
			return fmt.Errorf("mqtt broker %s: %w", name, err)
		}
		log.Info("using external mqtt broker", "broker", name, "host", b.Host, "port", client.Port(), "platform", platform)
		links = append(links, link{name, disc, client, client.Stop, func() any {
			return map[string]any{"host": b.Host, "port": client.Port(), "connected": client.Connected()}
		}})
	}
	var transports []mqtt.Transport
	for _, l := range links {
		if err := l.disc.Start(); err != nil {
			return fmt.Errorf("discovery on %s: %w", l.name, err)
		}
		transports = append(transports, l.transport)
	}
	var exporter *mqtt.Exporter
	if ex := cfg.MQTT.Export; ex != nil {
		var tr mqtt.Transport
		for _, l := range links {
			if l.name == ex.Broker {
				tr = l.transport
			}
		}
		if tr == nil {
			return fmt.Errorf("mqtt.export.broker %q is not a configured broker (embedded, external or a name from mqtt.brokers)", ex.Broker)
		}
		maxAge := 30 * time.Second
		if ex.CommandMaxAge != "" {
			if maxAge, err = time.ParseDuration(ex.CommandMaxAge); err != nil {
				return fmt.Errorf("mqtt.export.command_max_age: %w", err)
			}
		}
		name := ex.Name
		if name == "" {
			name = cfg.Homeassistant.Name
		}
		host, _ := os.Hostname()
		mqtt.Version = version
		exporter = mqtt.NewExporter(c, tr, mqtt.ExportOptions{Namespace: ex.Namespace, DiscoveryPrefix: ex.DiscoveryPrefix, Name: name, Include: ex.Include, Exclude: ex.Exclude, CommandMaxAge: maxAge}, core.Slugify(host), log.With("component", "export"))
		if err := exporter.Start(); err != nil {
			return fmt.Errorf("mqtt export: %w", err)
		}
		log.Info("exporting entities as an MQTT device", "broker", ex.Broker)
	}
	// Manual entities from configuration.yaml belong to the first broker
	// (the embedded one when enabled), as HA's mqtt: section does.
	if len(links) > 0 {
		for _, m := range cfg.MQTT.ManualEntities() {
			if err := links[0].disc.AddManual(m.Component, m.Config); err != nil {
				return fmt.Errorf("mqtt %s entity: %w", m.Component, err)
			}
		}
	}
	if err := restcmd.Register(c, cfg.RestCommand, log.With("component", "rest_command")); err != nil {
		return fmt.Errorf("rest_command: %w", err)
	}
	// Helpers restore their state before automations and template entities
	// read them.
	helperConfig := func(cfg *store.Config) helpers.Config {
		return helpers.Config{InputBoolean: cfg.InputBoolean, InputNumber: cfg.InputNumber, InputSelect: cfg.InputSelect, InputText: cfg.InputText, InputDatetime: cfg.InputDatetime, InputButton: cfg.InputButton, Counter: cfg.Counter, Timer: cfg.Timer, Location: loc}
	}
	helperSet, err := helpers.New(c, helperConfig(cfg), dir, log.With("component", "helpers"))
	if err != nil {
		return fmt.Errorf("helpers: %w", err)
	}
	// <domain>.reload re-reads configuration.yaml, as HA does.
	helperSet.SetLoader(func() (helpers.Config, error) {
		fresh, err := store.LoadConfig(cfgPath)
		if err != nil {
			return helpers.Config{}, err
		}
		return helperConfig(fresh), nil
	})
	engine := automation.New(c, automation.Options{Dir: dir, Location: loc, Latitude: cfg.Homeassistant.Latitude, Longitude: cfg.Homeassistant.Longitude, Transports: transports}, log.With("component", "automation"))
	if err := engine.Start(); err != nil {
		log.Error("automation files have errors", "err", err)
	}
	templates, err := virtual.NewTemplates(c, engine, cfg.Template, log.With("component", "template"))
	if err != nil {
		return fmt.Errorf("template: %w", err)
	}
	groups, err := virtual.NewGroups(c, cfg.Group, map[string][]map[string]any{"light": cfg.Light, "switch": cfg.Switch, "binary_sensor": cfg.BinarySensor, "sensor": cfg.Sensor, "cover": cfg.Cover, "fan": cfg.Fan}, log.With("component", "group"))
	if err != nil {
		return fmt.Errorf("group: %w", err)
	}
	mqttStatus := func() any {
		out := map[string]any{}
		for _, l := range links {
			out[l.name] = l.status()
		}
		return out
	}

	var rec *recorder.Recorder
	if cfg.Recorder.IsEnabled() {
		dbPath := cfg.Recorder.DBPath
		if dbPath == "" {
			dbPath = filepath.Join(dir, "history.db")
		}
		rec, err = recorder.New(c, recorder.Config{Path: dbPath, FlushInterval: cfg.Recorder.Flush(), PurgeKeepDays: cfg.Recorder.PurgeKeepDays, Include: cfg.Recorder.Include, Exclude: cfg.Recorder.Exclude}, log.With("component", "recorder"))
		if err != nil {
			return fmt.Errorf("recorder: %w", err)
		}
		log.Info("recorder enabled", "db", dbPath)
	}

	started := time.Now()
	status := func() map[string]any {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		st := map[string]any{
			"version": version, "uptime_seconds": int(time.Since(started).Seconds()),
			"entities": len(c.States()), "devices": len(c.Devices()), "mqtt": mqttStatus(),
			"heap_bytes": ms.HeapAlloc, "sys_bytes": ms.Sys, "goroutines": runtime.NumGoroutine(),
		}
		if rec != nil {
			st["recorder"] = rec.Stats()
		}
		return st
	}
	srv := api.New(c, auth, api.Info{
		LocationName: cfg.Homeassistant.Name, Version: version, TimeZone: cfg.Homeassistant.TimeZone,
		UnitSystem: cfg.Homeassistant.UnitSystem, Latitude: cfg.Homeassistant.Latitude, Longitude: cfg.Homeassistant.Longitude,
		Elevation: cfg.Homeassistant.Elevation, Currency: cfg.Homeassistant.Currency, ConfigDir: dir,
	}, web.FS(), status, log)
	srv.Automations = engine
	if rec != nil {
		srv.History = rec
	}
	// Hints tell the user which address devices should publish to: the
	// embedded broker on this host, or the first external broker.
	mqttHost := lanAddress()
	if !cfg.MQTT.EmbeddedEnabled() {
		if ebs := cfg.MQTT.ExternalBrokers(); len(ebs) > 0 {
			mqttHost = ebs[0].Host
		}
	}
	browser := netdiscovery.New(log.With("component", "netdiscovery"), netdiscovery.Options{MQTTHost: mqttHost, IsIntegrated: func(d netdiscovery.Device) bool {
		for _, dev := range c.Devices() {
			u, err := url.Parse(dev.ConfigurationURL)
			if err != nil || u.Hostname() == "" {
				continue
			}
			if strings.EqualFold(u.Hostname(), d.Host) || strings.EqualFold(u.Hostname(), strings.TrimSuffix(d.Host, ".")) {
				return true
			}
			for _, a := range d.Addrs {
				if a == u.Hostname() {
					return true
				}
			}
		}
		return false
	}})
	browser.Start()
	srv.Discovered = browser
	addr := net.JoinHostPort(cfg.HTTP.ServerHost, strconv.Itoa(cfg.HTTP.ServerPort))
	httpSrv := &http.Server{Addr: addr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("http: %w", err)
	}
	log.Info("http listening", "addr", addr)
	errCh := make(chan error, 1)
	if cfg.HTTP.SSLCertificate != "" {
		log.Info("http uses TLS", "certificate", cfg.HTTP.SSLCertificate)
		go func() { errCh <- httpSrv.ServeTLS(ln, cfg.HTTP.SSLCertificate, cfg.HTTP.SSLKey) }()
	} else {
		go func() { errCh <- httpSrv.Serve(ln) }()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	httpSrv.Shutdown(shutdownCtx)
	browser.Stop()
	if exporter != nil {
		exporter.Stop()
	}
	groups.Stop()
	templates.Stop()
	engine.Stop()
	helperSet.Stop()
	if sun != nil {
		sun.Stop()
	}
	persons.Stop()
	zones.Stop()
	if rec != nil {
		if err := rec.Stop(); err != nil {
			log.Error("recorder stop failed", "err", err)
		}
	}
	for _, l := range links {
		l.disc.Stop()
	}
	if err := registry.Flush(); err != nil {
		log.Error("registry save failed", "err", err)
	}
	var errs []error
	for _, l := range links {
		errs = append(errs, l.stop())
	}
	return errors.Join(errs...)
}

// lanAddress returns this host's first non-loopback IPv4 address, the
// one devices on the LAN can reach the embedded broker at.
func lanAddress() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, i := range ifaces {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := i.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() {
				return n.IP.String()
			}
		}
	}
	return ""
}
