/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package mqtt

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/probonopd/miniha/internal/template"
)

func init() {
	components["camera"] = newCamera
	components["image"] = newImage
}

const (
	defaultImageType = "image/jpeg"
	// A URL answering with more than this is not a still image.
	maxFetchedImage = 32 << 20
	fetchTimeout    = 10 * time.Second
)

// imageHolder keeps the last image under its own mutex, separate from the
// entity lock because the API reads it on every proxy request.
type imageHolder struct {
	mu          sync.Mutex
	data        []byte
	contentType string
}

func (h *imageHolder) set(data []byte, contentType string) {
	h.mu.Lock()
	h.data = slices.Clone(data)
	h.contentType = contentType
	h.mu.Unlock()
}

// Image implements core.ImageSource.
func (h *imageHolder) Image() ([]byte, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.data), h.contentType
}

func imageAttrs(entityID string) map[string]any {
	return map[string]any{"entity_picture": "/api/camera_proxy/" + entityID, "access_token": nil}
}

func imageEncoding(e *entity) (b64 bool, err error) {
	switch enc := e.cfg.Str("image_encoding"); enc {
	case "":
		return false, nil
	case "b64":
		return true, nil
	default:
		return false, fmt.Errorf("invalid image_encoding %q, only \"b64\" is supported", enc)
	}
}

// decodeImage turns a message payload into image bytes.
func decodeImage(payload []byte, b64 bool) ([]byte, error) {
	if !b64 {
		return payload, nil
	}
	return base64.StdEncoding.DecodeString(string(payload))
}

// --- camera ---

type mqttCamera struct {
	imageHolder
}

func newCamera(e *entity) (component, error) {
	if !e.cfg.Has("topic") {
		return nil, errMissing("topic")
	}
	b64, err := imageEncoding(e)
	if err != nil {
		return nil, err
	}
	c := &mqttCamera{}
	// HA cameras always serve JPEG from camera.mqtt and report idle.
	c.contentType = defaultImageType
	e.attrs = func() map[string]any { return imageAttrs(e.entityID) }
	e.subscribe(e.cfg.Str("topic"), func(m Message) {
		data, err := decodeImage(m.Payload, b64)
		if err != nil {
			e.d.log.Warn("camera payload is not valid base64", "entity_id", e.entityID, "err", err)
			return
		}
		c.set(data, defaultImageType)
	})
	e.d.core.RegisterController(e.entityID, c)
	e.setState("idle", context.Background())
	return c, nil
}

func (c *mqttCamera) stop() {}

// --- image ---

type mqttImage struct {
	imageHolder
	e           *entity
	defaultType string
	urlTpl      *template.Template
	client      *http.Client
	ctx         context.Context
	cancel      context.CancelFunc

	// Fetches run concurrently; only the newest request may store its
	// result, so a slow old download cannot overwrite a newer image.
	seqMu sync.Mutex
	seq   uint64
}

func newImage(e *entity) (component, error) {
	cfg := e.cfg
	if cfg.Has("image_topic") == cfg.Has("url_topic") {
		return nil, fmt.Errorf("exactly one of image_topic and url_topic is required")
	}
	if cfg.Has("url_topic") && cfg.Has("image_encoding") {
		return nil, fmt.Errorf("image_encoding only applies to image_topic")
	}
	if cfg.Has("image_topic") && cfg.Has("url_template") {
		return nil, fmt.Errorf("url_template only applies to url_topic")
	}
	b64, err := imageEncoding(e)
	if err != nil {
		return nil, err
	}
	urlTpl, err := cfg.Template("url_template")
	if err != nil {
		return nil, err
	}
	img := &mqttImage{e: e, defaultType: cfg.StrDef("content_type", defaultImageType), urlTpl: urlTpl, client: &http.Client{Timeout: fetchTimeout}}
	img.ctx, img.cancel = context.WithCancel(context.Background())
	e.attrs = func() map[string]any { return imageAttrs(e.entityID) }
	if cfg.Has("image_topic") {
		e.subscribe(cfg.Str("image_topic"), func(m Message) {
			data, err := decodeImage(m.Payload, b64)
			if err != nil {
				e.d.log.Warn("image payload is not valid base64", "entity_id", e.entityID, "err", err)
				return
			}
			img.store(data, img.defaultType)
		})
	} else {
		e.subscribe(cfg.Str("url_topic"), img.onURL)
	}
	e.d.core.RegisterController(e.entityID, img)
	e.write(false)
	return img, nil
}

func (i *mqttImage) stop() { i.cancel() }

func (i *mqttImage) store(data []byte, contentType string) {
	i.set(data, contentType)
	i.e.setState(activationTimestamp(i.e.d.core.Now()), context.Background())
}

// onURL downloads off the broker callback so a slow server cannot stall
// message delivery for every other entity.
func (i *mqttImage) onURL(m Message) {
	rawURL := string(m.Payload)
	if i.urlTpl != nil {
		var err error
		rawURL, err = i.e.render(i.urlTpl, m.Payload)
		if err != nil {
			i.e.d.log.Warn("image url_template failed", "entity_id", i.e.entityID, "err", err)
			return
		}
	}
	if rawURL == "" || rawURL == "None" {
		return
	}
	i.seqMu.Lock()
	i.seq++
	seq := i.seq
	i.seqMu.Unlock()
	go func() {
		data, ct, err := i.fetch(rawURL)
		if err != nil {
			i.e.d.log.Warn("image download failed", "entity_id", i.e.entityID, "url", rawURL, "err", err)
			return
		}
		i.seqMu.Lock()
		defer i.seqMu.Unlock()
		if seq != i.seq {
			return
		}
		i.store(data, ct)
	}()
}

func (i *mqttImage) fetch(rawURL string) ([]byte, string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, "", fmt.Errorf("unsupported URL scheme %q", u.Scheme)
	}
	req, err := http.NewRequestWithContext(i.ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := i.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, "", fmt.Errorf("HTTP status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchedImage+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > maxFetchedImage {
		return nil, "", fmt.Errorf("image larger than %d bytes", maxFetchedImage)
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = i.defaultType
	}
	return data, ct, nil
}
