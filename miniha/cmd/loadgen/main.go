/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

// Command loadgen drives an MQTT broker the way SPEC section 10 describes:
// retained Home Assistant discovery for N sensors, then random states at an
// aggregate rate. It exists so the resource budget can be measured on a
// real MiniHA process instead of guessed.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
)

// entitiesPerDevice groups sensors into device blocks, as real integrations
// do, so the hub also exercises its device registry.
const entitiesPerDevice = 10

type device struct {
	Identifiers []string `json:"identifiers"`
	Name        string   `json:"name"`
}

type sensorConfig struct {
	Name       string `json:"name"`
	UniqueID   string `json:"unique_id"`
	StateTopic string `json:"state_topic"`
	Unit       string `json:"unit_of_measurement"`
	Device     device `json:"device"`
}

func main() {
	broker := flag.String("broker", "127.0.0.1:1883", "MQTT broker host:port")
	entities := flag.Int("entities", 300, "number of synthetic sensors")
	rate := flag.Int("rate", 10, "state messages per second, summed over all sensors")
	duration := flag.Duration("duration", 60*time.Second, "how long to publish states")
	flag.Parse()
	if *entities < 1 || *rate < 1 {
		fmt.Fprintln(os.Stderr, "loadgen: -entities and -rate must be at least 1")
		os.Exit(2)
	}
	if err := run(*broker, *entities, *rate, *duration); err != nil {
		fmt.Fprintln(os.Stderr, "loadgen:", err)
		os.Exit(1)
	}
}

func run(broker string, entities, rate int, duration time.Duration) error {
	u, err := url.Parse("mqtt://" + broker)
	if err != nil {
		return err
	}
	ctx := context.Background()
	cm, err := autopaho.NewConnection(ctx, autopaho.ClientConfig{
		ServerUrls:        []*url.URL{u},
		KeepAlive:         30,
		ConnectRetryDelay: time.Second,
		ClientConfig:      paho.ClientConfig{ClientID: fmt.Sprintf("loadgen-%d", os.Getpid())},
	})
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = cm.AwaitConnection(cctx)
	cancel()
	if err != nil {
		return fmt.Errorf("connect to %s: %w", broker, err)
	}

	// Discovery is published with QoS 1 and retained, so the hub has every
	// entity registered before the first state arrives.
	for i := range entities {
		payload, err := json.Marshal(sensorConfig{
			Name:       fmt.Sprintf("Load sensor %d", i),
			UniqueID:   fmt.Sprintf("load_%d", i),
			StateTopic: fmt.Sprintf("load/%d/state", i),
			Unit:       "°C",
			Device: device{
				Identifiers: []string{fmt.Sprintf("load_dev_%d", i/entitiesPerDevice)},
				Name:        fmt.Sprintf("Load device %d", i/entitiesPerDevice),
			},
		})
		if err != nil {
			return err
		}
		topic := fmt.Sprintf("homeassistant/sensor/load_%d/config", i)
		if err := publish(cm, topic, payload, 1, true); err != nil {
			return err
		}
	}
	fmt.Fprintf(os.Stderr, "loadgen: discovery for %d sensors published\n", entities)

	ticker := time.NewTicker(time.Second / time.Duration(rate))
	defer ticker.Stop()
	deadline := time.After(duration)
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	sent := 0
	for done := false; !done; {
		select {
		case <-deadline:
			done = true
		case <-ticker.C:
			state := strconv.FormatFloat(15+rng.Float64()*15, 'f', 1, 64)
			topic := "load/" + strconv.Itoa(rng.Intn(entities)) + "/state"
			if err := publish(cm, topic, []byte(state), 0, false); err != nil {
				return err
			}
			sent++
		}
	}
	fmt.Fprintf(os.Stderr, "loadgen: %d states published in %s\n", sent, duration)

	dctx, dcancel := context.WithTimeout(ctx, 3*time.Second)
	defer dcancel()
	return cm.Disconnect(dctx)
}

func publish(cm *autopaho.ConnectionManager, topic string, payload []byte, qos byte, retain bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := cm.Publish(ctx, &paho.Publish{Topic: topic, Payload: payload, QoS: qos, Retain: retain})
	if err != nil {
		return fmt.Errorf("publish %s: %w", topic, err)
	}
	return nil
}
