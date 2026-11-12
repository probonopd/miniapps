/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package mqtt

import "time"

type timerHandle struct{ t *time.Timer }

// after (re)arms a one-shot timer; the previous one is cancelled.
func after(prev *timerHandle, secs float64, fn func()) *timerHandle {
	if prev != nil && prev.t != nil {
		prev.t.Stop()
	}
	return &timerHandle{time.AfterFunc(time.Duration(secs*float64(time.Second)), fn)}
}
