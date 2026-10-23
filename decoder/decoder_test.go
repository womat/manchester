package decoder

import (
	"testing"
	"time"
)

func TestWithinTolerance(t *testing.T) {
	tests := []struct {
		name      string
		value     time.Duration
		reference time.Duration
		tolerance time.Duration
		expected  bool
	}{
		{"Exact Match", 100 * time.Millisecond, 100 * time.Millisecond, 10 * time.Millisecond, true},
		{"Within Upper Bound", 105 * time.Millisecond, 100 * time.Millisecond, 10 * time.Millisecond, true},
		{"Within Lower Bound", 95 * time.Millisecond, 100 * time.Millisecond, 10 * time.Millisecond, true},
		{"Outside Upper Bound", 111 * time.Millisecond, 100 * time.Millisecond, 10 * time.Millisecond, false},
		{"Outside Lower Bound", 89 * time.Millisecond, 100 * time.Millisecond, 10 * time.Millisecond, false},
		{"Outside Lower Bound", 50 * time.Millisecond, 100 * time.Millisecond, 10 * time.Millisecond, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := withinTolerance(tt.value, tt.reference, tt.tolerance)
			if result != tt.expected {
				t.Errorf("withinTolerance(%v, %v, %v) = %v; expected %v",
					tt.value, tt.reference, tt.tolerance, result, tt.expected)
			}
		})
	}
}

// TestDecoderInfoIsConcurrencySafe guards the promise Info() makes in its own
// documentation. Info() used to read the decoder state and the bit period
// without synchronisation while the decoding goroutine was writing them;
// run this with -race to see the regression.
func TestDecoderInfoIsConcurrencySafe(t *testing.T) {
	const events = 2000

	c := make(chan Event, 64)
	d, err := New(c, 0) // no bit clock: clock recovery writes the state
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer d.Close()

	go func() {
		for b := range d.Bits() {
			_ = b
		}
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		now := time.Now()
		for i := range events {
			edge := RisingEdge
			if i%2 == 1 {
				edge = FallingEdge
			}
			c <- Event{Time: now.Add(time.Duration(i) * 5 * time.Millisecond), Edge: edge}
		}
	}()

	for range events {
		_ = d.Info()
	}
	<-done
}

// TestDecoderNewRejectsInvalidConfig is the counterpart to the encoder's
// TestEncoderNewRejectsInvalidConfig: New used to accept a nil event
// channel, which left its goroutine blocked forever, and a negative bit clock,
// which silently meant "discover the clock" instead of being reported.
func TestDecoderNewRejectsInvalidConfig(t *testing.T) {
	events := make(chan Event)

	tests := []struct {
		name    string
		events  <-chan Event
		clockHz int
		opts    []Option
	}{
		{"nil event channel", nil, testClockHz, nil},
		{"negative bit clock", events, -1, nil},
		{"unknown encoding", events, testClockHz, []Option{WithManchesterEncoding(42)}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := New(tt.events, tt.clockHz, tt.opts...)
			if err == nil {
				d.Close()
				t.Fatal("New accepted an invalid configuration, want an error")
			}
			if d != nil {
				t.Errorf("New returned a non-nil decoder alongside the error %v", err)
			}
		})
	}
}

// TestDecoderNewAcceptsValidConfig pins down that a zero bit clock stays valid:
// it selects clock recovery rather than being rejected as a missing value.
func TestDecoderNewAcceptsValidConfig(t *testing.T) {
	tests := []struct {
		name    string
		clockHz int
	}{
		{"fixed bit clock", testClockHz},
		{"clock recovery", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := New(make(chan Event), tt.clockHz)
			if err != nil {
				t.Fatalf("New rejected a valid configuration: %v", err)
			}
			if err := d.Close(); err != nil {
				t.Errorf("Close: %v", err)
			}
		})
	}
}
