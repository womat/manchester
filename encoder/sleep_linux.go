//go:build linux

package encoder

import (
	"time"

	"golang.org/x/sys/unix"
)

// prepareThread reduces the timer slack of the calling thread to 1ns.
//
// Linux lets the kernel end a normal thread's sleep up to 50µs late, to batch
// wakeups. At 2 kHz that is a fifth of every half-bit. The setting applies to
// the calling thread only, so the caller must have locked its goroutine to it.
// A failure leaves the default slack in place, which only costs precision.
func prepareThread() {
	_ = unix.Prctl(unix.PR_SET_TIMERSLACK, 1, 0, 0, 0)
}

// sleepPrecise blocks until deadline in nanosleep, which the kernel ends on a
// high-resolution timer instead of the Go runtime's millisecond granularity.
//
// The remaining time is recomputed after every interruption: the runtime
// preempts goroutines with signals, so EINTR is routine here.
func sleepPrecise(deadline time.Time) {
	for {
		d := time.Until(deadline)
		if d <= 0 {
			return
		}
		ts := unix.NsecToTimespec(d.Nanoseconds())
		if err := unix.Nanosleep(&ts, nil); err != unix.EINTR {
			return
		}
	}
}
