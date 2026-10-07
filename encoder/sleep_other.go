//go:build !linux

package encoder

import "time"

// prepareThread does nothing off Linux.
func prepareThread() {}

// sleepPrecise blocks until deadline. Off Linux it falls back to
// time.Sleep and inherits the granularity of the runtime's timers.
func sleepPrecise(deadline time.Time) {
	time.Sleep(time.Until(deadline))
}
