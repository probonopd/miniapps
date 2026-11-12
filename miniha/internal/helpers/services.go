/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package helpers

import (
	"context"
	"errors"
	"fmt"

	"github.com/probonopd/miniha/internal/core"
)

func fld(desc string, required bool) map[string]any {
	return map[string]any{"description": desc, "required": required}
}

// svc registers a service that runs fn for every targeted entity of the
// domain. core's own helper for this is unexported, and the helpers need
// the concrete type behind the entity, not a controller interface.
func svc[T helper](h *Helpers, domain, name, title string, fields map[string]any, fn func(ctx context.Context, e T, data map[string]any) error) {
	h.c.RegisterService(domain, name, func(ctx context.Context, call core.ServiceCall) error {
		if len(call.EntityIDs) == 0 {
			return fmt.Errorf("%s.%s: no entity_id given", domain, name)
		}
		var errs []error
		for _, id := range call.EntityIDs {
			if d, _ := core.SplitEntityID(id); d != domain {
				errs = append(errs, fmt.Errorf("%s is not a %s entity", id, domain))
				continue
			}
			e, ok := h.lookup(id).(T)
			if !ok {
				errs = append(errs, fmt.Errorf("%s does not exist", id))
				continue
			}
			if err := fn(ctx, e, call.Data); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", id, err))
			}
		}
		return errors.Join(errs...)
	}, core.ServiceDesc{Name: title, Fields: fields, Target: map[string]any{"entity": map[string]any{"domain": []string{domain}}}})
}

func (h *Helpers) registerServices() {
	h.c.RegisterOnOffDomain(domInputBoolean)
	for _, d := range Domains {
		domain := d
		h.c.RegisterService(domain, "reload", func(ctx context.Context, _ core.ServiceCall) error {
			h.loadMu.Lock()
			load := h.loader
			h.loadMu.Unlock()
			if load == nil {
				return fmt.Errorf("%s.reload: no configuration loader installed", domain)
			}
			cfg, err := load()
			if err != nil {
				return fmt.Errorf("%s.reload: %w", domain, err)
			}
			return h.reload(cfg, []string{domain})
		}, core.ServiceDesc{Name: "Reload", Description: "Reloads " + domain + " helpers from the configuration"})
	}
	h.registerInputNumberServices()
	h.registerInputSelectServices()
	h.registerInputTextServices()
	h.registerInputDatetimeServices()
	h.registerInputButtonServices()
	h.registerCounterServices()
	h.registerTimerServices()
}
