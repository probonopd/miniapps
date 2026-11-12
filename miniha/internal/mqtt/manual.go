/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package mqtt

import (
	"fmt"
	"strings"
)

// AddManual installs an entity declared under mqtt: in configuration.yaml.
// It takes the same options as a discovery payload, so the YAML is turned
// into the same Config and goes through the same constructors. Manual
// entities have no discovery topic; their id is "manual/<component>/<n>"
// with n counting per component in configuration order.
func (d *Discovery) AddManual(component string, cfg map[string]any) error {
	ctor, ok := components[component]
	if !ok {
		return fmt.Errorf("mqtt manual entity: unsupported component %q", component)
	}
	prefix := "manual/" + component + "/"
	d.mu.Lock()
	n := 0
	for id := range d.items {
		if strings.HasPrefix(id, prefix) {
			n++
		}
	}
	d.mu.Unlock()
	id := fmt.Sprintf("%s%d", prefix, n)
	d.install(id, component, expand(cfg), ctor)
	// install reports constructor errors only to the log.
	d.mu.Lock()
	_, installed := d.items[id]
	d.mu.Unlock()
	if !installed {
		return fmt.Errorf("mqtt manual entity %s: invalid configuration (see log)", id)
	}
	return nil
}
