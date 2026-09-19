// Package manchester_test contains the integration tests that tie the encoder
// and the decoder together. They live in their own directory because neither
// package may import the other: the encoder writes through a SetValue callback
// and the decoder reads from an Event channel, and that decoupling is what
// keeps the codec usable over any transport.
//
// The tests wire both ends into a virtual line: every level change reported by
// the encoder becomes an edge event for the decoder, exactly as a GPIO pin
// would deliver it.
package manchester_test

import (
	"testing"
	"time"

	"github.com/womat/golib/manchester/decoder"
	"github.com/womat/golib/manchester/encoder"
)

const (
	// bitClockHz is the bit clock used by the round-trip tests. Their edge
	// timestamps are synthetic, so the clock only determines how long the
	// encoder takes in real time - a fast clock keeps the suite short.
	bitClockHz = 1000

	// timingClockHz is used where wall-clock timing is measured. It is the bit
	// clock the demos default to, and its half-bit period of 10ms is well above
	// the scheduling jitter of a loaded machine.
	timingClockHz = 50
)

// line is a virtual connection between an encoder and a decoder.
//
// Timestamps are synthetic: the encoder calls SetValue exactly once per
// half-bit, so counting the calls yields a perfectly timed edge stream. That
// keeps the round-trip tests independent of the host's scheduling accuracy -
// encoder timing is covered separately by TestEncoderKeepsHalfBitPeriod.
type line struct {
	halfBit   time.Duration
	base      time.Time
	step      int
	lastLevel encoder.Level
	events    []decoder.Event
}

func newLine() *line {
	return &line{
		halfBit:   time.Second / bitClockHz / 2,
		base:      time.Now(),
		lastLevel: -1, // no level driven yet
	}
}

// setValue records an edge whenever the driven level actually changes.
func (l *line) setValue(level encoder.Level) error {
	if level != l.lastLevel {
		if l.lastLevel != -1 {
			edge := decoder.RisingEdge
			if level == encoder.Low {
				edge = decoder.FallingEdge
			}
			l.events = append(l.events, decoder.Event{
				Time: l.base.Add(time.Duration(l.step) * l.halfBit),
				Edge: edge,
			})
		}
		l.lastLevel = level
	}
	l.step++
	return nil
}

// transmit encodes data and returns the resulting edge stream.
func transmit(t *testing.T, data []byte, enc encoder.ManchesterEncoding, order encoder.BitOrder, syncBytes int) []decoder.Event {
	t.Helper()

	l := newLine()
	e := encoder.New(bitClockHz, l.setValue,
		encoder.WithManchesterEncoding(enc),
		encoder.WithBitOrder(order),
		encoder.WithSyncBytes(syncBytes),
		encoder.WithErrorHandler(func(err error) { t.Errorf("encoder error: %v", err) }),
	)

	if _, err := e.Send(data); err != nil {
		t.Fatalf("Send: %v", err)
	}
	e.Wait()
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	return l.events
}

// receive feeds an edge stream into a decoder and collects the decoded bits.
func receive(t *testing.T, events []decoder.Event, enc decoder.ManchesterEncoding) []decoder.Bit {
	t.Helper()

	c := make(chan decoder.Event, len(events)+1)
	for _, e := range events {
		c <- e
	}

	d, err := decoder.New(c, bitClockHz, decoder.WithManchesterEncoding(enc))
	if err != nil {
		t.Fatalf("decoder.New: %v", err)
	}

	var bits []decoder.Bit
	done := make(chan struct{})
	go func() {
		defer close(done)
		for b := range d.Bits() {
			bits = append(bits, b)
		}
	}()

	// Give the decoder goroutine time to drain the pre-filled event channel.
	waitFor(t, func() bool { return len(c) == 0 })
	if err := d.Close(); err != nil {
		t.Fatalf("decoder.Close: %v", err)
	}
	<-done

	return bits
}

// waitFor polls cond until it holds or the test times out.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the decoder to drain the event channel")
		}
		time.Sleep(time.Millisecond)
	}
}

// assemble reconstructs bytes from a bit stream using the framing the encoder
// applies to data bytes: a low start bit, eight data bits, a high stop bit.
func assemble(bits []decoder.Bit, order encoder.BitOrder) []byte {
	var out []byte
	var b byte
	var n int

	for _, bit := range bits {
		if bit == decoder.Invalid {
			b, n = 0, 0
			continue
		}

		switch n {
		case 0: // start bit must be low
			if bit == decoder.Low {
				b, n = 0, 1
			}
		case 9: // stop bit must be high
			if bit == decoder.High {
				out = append(out, b)
			}
			b, n = 0, 0
		default:
			if order == encoder.LSBFirst {
				b |= byte(bit) << (n - 1)
			} else {
				b |= byte(bit) << (8 - n)
			}
			n++
		}
	}

	return out
}

// TestRoundTrip is the regression test for the swapped decoding tables:
// the decoder used to return the bitwise complement of the transmitted data,
// which made the start bit unrecognisable and the payload unrecoverable.
func TestRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		enc   encoder.ManchesterEncoding
		dec   decoder.ManchesterEncoding
		order encoder.BitOrder
		data  []byte
	}{
		{"IEEE/LSB", encoder.IEEE, decoder.IEEE, encoder.LSBFirst, []byte("Hello World")},
		{"IEEE/MSB", encoder.IEEE, decoder.IEEE, encoder.MSBFirst, []byte("Hello World")},
		{"Thomas/LSB", encoder.Thomas, decoder.Thomas, encoder.LSBFirst, []byte("Hello World")},
		{"Thomas/MSB", encoder.Thomas, decoder.Thomas, encoder.MSBFirst, []byte("Hello World")},
		{"IEEE/binary", encoder.IEEE, decoder.IEEE, encoder.LSBFirst, []byte{0x00, 0xff, 0x55, 0xaa, 0x01, 0x80}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := transmit(t, tt.data, tt.enc, tt.order, 2)
			bits := receive(t, events, tt.dec)
			got := assemble(bits, tt.order)

			if string(got) != string(tt.data) {
				t.Errorf("round trip returned %q (% 08b), want %q (% 08b)", got, got, tt.data, tt.data)
			}
		})
	}
}

// TestEncodingIsNotInverted pins down the polarity of both conventions:
// IEEE 802.3 encodes a logical 1 as a low-to-high transition at mid-bit,
// G.E. Thomas as a high-to-low transition. A sync byte is eight logical ones,
// so the decoder must report High for all of them.
//
// The first edge only establishes the reference timestamp, so the first bit of
// a transmission is never decoded - hence one bit less than transmitted.
func TestEncodingIsNotInverted(t *testing.T) {
	tests := []struct {
		name string
		enc  encoder.ManchesterEncoding
		dec  decoder.ManchesterEncoding
	}{
		{"IEEE", encoder.IEEE, decoder.IEEE},
		{"Thomas", encoder.Thomas, decoder.Thomas},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// One sync byte (0xff, sent without start/stop bits) and no payload.
			events := transmit(t, nil, tt.enc, encoder.LSBFirst, 1)
			bits := receive(t, events, tt.dec)

			if len(bits) != 7 {
				t.Fatalf("decoded %d bits from one sync byte, want 7: %v", len(bits), bits)
			}
			for i, b := range bits {
				if b != decoder.High {
					t.Errorf("sync bit %d decoded as %v, want High (sequence: %v)", i, b, bits)
				}
			}
		})
	}
}

// TestCrossEncodingIsRejected guards against the two tables being swapped
// again: decoding IEEE data with the Thomas table must not reproduce the
// payload.
func TestCrossEncodingIsRejected(t *testing.T) {
	data := []byte("Hello World")

	events := transmit(t, data, encoder.IEEE, encoder.LSBFirst, 2)
	got := assemble(receive(t, events, decoder.Thomas), encoder.LSBFirst)

	if string(got) == string(data) {
		t.Errorf("IEEE data decoded correctly with the Thomas table - the decoding tables are swapped")
	}
}

// TestEncoderKeepsHalfBitPeriod is the regression test for the free-running
// ticker: it was started once in New() and never reset, so after any idle
// period a buffered tick truncated the first half-bits of the next message -
// exactly the sync preamble the decoder needs to lock on.
//
// The check is deliberately one-sided: a half-bit that is too short is the
// regression being guarded against, while a late timer is an artefact of the
// host's scheduler and says nothing about the encoder. The bound is the one the
// decoder itself applies (25%).
func TestEncoderKeepsHalfBitPeriod(t *testing.T) {
	const idle = 37 * time.Millisecond

	halfBit := time.Second / timingClockHz / 2
	tolerance := halfBit * 25 / 100

	var times []time.Time
	e := encoder.New(timingClockHz, func(encoder.Level) error {
		times = append(times, time.Now())
		return nil
	}, encoder.WithSyncBytes(0))
	defer e.Close()

	if _, err := e.Send([]byte{0x00}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	e.Wait()

	// Idle in between, so a free-running ticker has a tick waiting.
	time.Sleep(idle)
	mark := len(times)

	if _, err := e.Send([]byte{0x00}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	e.Wait()

	if len(times)-mark < 2 {
		t.Fatalf("second message produced %d half-bit steps, want at least 2", len(times)-mark)
	}

	for i := mark; i+1 < len(times); i++ {
		if d := times[i+1].Sub(times[i]); d < halfBit-tolerance {
			t.Errorf("half-bit %d of the second message was truncated to %v, want at least %v",
				i-mark+1, d.Round(100*time.Microsecond), halfBit-tolerance)
		}
	}
}
