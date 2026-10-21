package decoder

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// run starts a decoder on an unbuffered channel, so every send returns once
// the decoder has taken the edge, and collects its bits until it is closed.
func run(t *testing.T, bitClockHz int) (*Decoder, chan<- Event, func() []Bit) {
	t.Helper()

	c := make(chan Event)
	d, err := New(c, bitClockHz, WithManchesterEncoding(IEEE), WithBufferSize(8192))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	done := make(chan []Bit)
	go func() {
		var got []Bit
		for b := range d.Bits() {
			got = append(got, b)
		}
		done <- got
	}()
	return d, c, func() []Bit {
		d.Close()
		return <-done
	}
}

// TestResetDiscardsARecoveredClock covers the caller that notices a decoder
// delivering nothing useful: Reset returns it to clock recovery, and the next
// transmission is decoded at its own rate.
func TestResetDiscardsARecoveredClock(t *testing.T) {
	d, c, stop := run(t, 0)

	for _, e := range newSignal(IEEE, constant(100)).send(frame(1, 30)...).events {
		c <- e
	}
	if info := d.Info(); !strings.Contains(info, "100.00 Hz") {
		t.Fatalf("Info() = %q before the reset, want the clock of the first transmission", info)
	}

	d.Reset()
	if info := d.Info(); !strings.Contains(info, "discovering clock") {
		t.Errorf("Info() = %q after Reset, want clock recovery", info)
	}

	// Starts after the first transmission has ended.
	s := newSignal(IEEE, constant(50)).pause(time.Second)
	want := frame(2, 100)
	for _, e := range s.send(want...).events {
		c <- e
	}
	if info := d.Info(); !strings.Contains(info, "50.00 Hz") {
		t.Errorf("Info() = %q, want the clock of the second transmission", info)
	}

	got := stop()
	if len(got) < len(want) || !slices.Equal(got[len(got)-len(want):], want) {
		t.Errorf("the transmission after Reset was not decoded in full, %d bits in total", len(got))
	}
}

// TestResetKeepsAConfiguredClock makes sure Reset goes back to how the decoder
// was created: with a configured bit clock it does not start recovering one.
func TestResetKeepsAConfiguredClock(t *testing.T) {
	d, _, stop := run(t, 50)
	defer stop()

	d.Reset()

	if info := d.Info(); !strings.Contains(info, "decoding data") || !strings.Contains(info, "50.00 Hz") {
		t.Errorf("Info() = %q after Reset, want the configured 50 Hz", info)
	}
}

// TestResetAfterClose must not block: the decoder that would apply the reset
// is gone.
func TestResetAfterClose(t *testing.T) {
	d, _, stop := run(t, 0)
	stop()

	finished := make(chan struct{})
	go func() {
		d.Reset()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("Reset blocked on a closed decoder")
	}
}
