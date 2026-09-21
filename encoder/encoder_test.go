package encoder

import (
	"errors"
	"testing"
	"time"
)

// errFailingLine simulates a GPIO write that fails.
var errFailingLine = errors.New("line failure")

// unitTestClockHz keeps the half-bit period short: these tests inspect the
// level sequence, not the timing, and the timing itself is covered by the
// integration tests in the parent directory.
const unitTestClockHz = 100_000

// recorder captures the levels an encoder drives onto the line.
type recorder struct {
	levels []Level
}

func (r *recorder) setValue(l Level) error {
	r.levels = append(r.levels, l)
	return nil
}

// newRecordingEncoder returns an encoder writing into a recorder.
func newRecordingEncoder(t *testing.T, opts ...Option) (*Encoder, *recorder) {
	t.Helper()

	r := &recorder{}
	e, err := New(unitTestClockHz, r.setValue, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { e.Close() })

	return e, r
}

// halfBits spells out a bit sequence as the half-bit levels it is transmitted
// as, stating the two conventions independently of the encoder's own table:
// IEEE 802.3 sends a logical 1 as low-then-high, G.E. Thomas as high-then-low.
func halfBits(t *testing.T, enc ManchesterEncoding, bits ...byte) []Level {
	t.Helper()

	var out []Level
	for _, b := range bits {
		if (b == 1) == (enc == IEEE) {
			out = append(out, Low, High)
		} else {
			out = append(out, High, Low)
		}
	}

	return out
}

func assertLevels(t *testing.T, got, want []Level) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("encoder drove %d half-bits, want %d\ngot:  %v\nwant: %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("half-bit %d is %v, want %v\ngot:  %v\nwant: %v", i, got[i], want[i], got, want)
		}
	}
}

func TestEncodeByteFraming(t *testing.T) {
	// 0x41 = 0b0100_0001
	const b = 0x41

	tests := []struct {
		name         string
		order        BitOrder
		encoding     ManchesterEncoding
		addStartStop bool
		bits         []byte
	}{
		{
			name:         "LSB first with start and stop bit",
			order:        LSBFirst,
			encoding:     IEEE,
			addStartStop: true,
			bits: []byte{
				0,                      // start bit
				1, 0, 0, 0, 0, 0, 1, 0, // 0x41, least significant bit first
				1, // stop bit
			},
		},
		{
			name:         "MSB first with start and stop bit",
			order:        MSBFirst,
			encoding:     IEEE,
			addStartStop: true,
			bits: []byte{
				0,                      // start bit
				0, 1, 0, 0, 0, 0, 0, 1, // 0x41, most significant bit first
				1, // stop bit
			},
		},
		{
			name:         "LSB first without framing",
			order:        LSBFirst,
			encoding:     IEEE,
			addStartStop: false,
			bits:         []byte{1, 0, 0, 0, 0, 0, 1, 0}, // data only, no framing
		},
		{
			name:         "Thomas inverts the half-bit levels",
			order:        LSBFirst,
			encoding:     Thomas,
			addStartStop: true,
			bits: []byte{
				0,                      // start bit
				1, 0, 0, 0, 0, 0, 1, 0, // 0x41, least significant bit first
				1, // stop bit
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, r := newRecordingEncoder(t,
				WithBitOrder(tt.order),
				WithManchesterEncoding(tt.encoding),
			)

			e.encodeByte(b, tt.addStartStop)

			assertLevels(t, r.levels, halfBits(t, tt.encoding, tt.bits...))
		})
	}
}

// TestEncodeBitIsBalanced pins down the defining property of Manchester
// coding: every bit consists of two half-bits of opposite level, so the line
// carries no DC component and every bit has a mid-bit transition.
func TestEncodeBitIsBalanced(t *testing.T) {
	for _, enc := range []ManchesterEncoding{IEEE, Thomas} {
		for _, bit := range []byte{0, 1} {
			e, r := newRecordingEncoder(t, WithManchesterEncoding(enc))

			e.encodeBit(bit)

			if len(r.levels) != 2 {
				t.Fatalf("encoding %v bit %d drove %d half-bits, want 2", enc, bit, len(r.levels))
			}
			if r.levels[0] == r.levels[1] {
				t.Errorf("encoding %v bit %d has no mid-bit transition: %v", enc, bit, r.levels)
			}
		}
	}
}

func TestEncodingTable(t *testing.T) {
	tests := []struct {
		name     string
		encoding ManchesterEncoding
		one      [2]Level
		zero     [2]Level
	}{
		// IEEE 802.3: a logical 1 is a low-to-high transition at mid-bit.
		{"IEEE", IEEE, [2]Level{Low, High}, [2]Level{High, Low}},
		// G.E. Thomas uses the opposite convention.
		{"Thomas", Thomas, [2]Level{High, Low}, [2]Level{Low, High}},
		// Unsupported values are rejected by New(), the table falls back to IEEE.
		{"unknown falls back to IEEE", ManchesterEncoding(42), [2]Level{Low, High}, [2]Level{High, Low}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			table := encodingTable(tt.encoding)

			if got := table[High]; got != tt.one {
				t.Errorf("logical 1 is encoded as %v, want %v", got, tt.one)
			}
			if got := table[Low]; got != tt.zero {
				t.Errorf("logical 0 is encoded as %v, want %v", got, tt.zero)
			}
		})
	}
}

func TestSyncBytes(t *testing.T) {
	tests := []struct {
		name     string
		opts     []Option
		expected int
	}{
		{"two sync bytes by default", nil, 2},
		{"WithSyncBytes", []Option{WithSyncBytes(5)}, 5},
		{"WithSyncBytes(0)", []Option{WithSyncBytes(0)}, 0},
		{"WithoutSync", []Option{WithoutSync()}, 0},
		{"a negative count is ignored", []Option{WithSyncBytes(-1)}, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, r := newRecordingEncoder(t, tt.opts...)

			if _, err := e.Send(nil); err != nil {
				t.Fatalf("Send: %v", err)
			}
			e.Wait()

			// A sync byte is 0xff sent without start and stop bits: 8 bits,
			// two half-bits each.
			if want := tt.expected * 8 * 2; len(r.levels) != want {
				t.Errorf("preamble drove %d half-bits, want %d (%d sync bytes)", len(r.levels), want, tt.expected)
			}
		})
	}
}

func TestSendReportsTheNumberOfDataBytes(t *testing.T) {
	e, _ := newRecordingEncoder(t, WithSyncBytes(3))

	data := []byte("golib")
	n, err := e.Send(data)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	e.Wait()

	// Sync bytes are transmitted but are not part of the caller's data.
	if n != len(data) {
		t.Errorf("Send reported %d bytes, want %d", n, len(data))
	}
}

func TestSetBitReportsLineErrors(t *testing.T) {
	var got error
	failing := func(Level) error { return errFailingLine }

	e, err := New(unitTestClockHz, failing,
		WithErrorHandler(func(err error) { got = err }),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer e.Close()

	e.encodeBit(1)

	if got == nil {
		t.Fatal("a failing setValue did not reach the error handler")
	}
	if !errors.Is(got, errFailingLine) {
		t.Errorf("error handler received %v, want it to wrap %v", got, errFailingLine)
	}
}

// TestSetBitWithoutErrorHandler covers the documented default: line errors are
// ignored when no handler is installed.
func TestSetBitWithoutErrorHandler(t *testing.T) {
	e, err := New(unitTestClockHz, func(Level) error { return errFailingLine })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer e.Close()

	e.encodeBit(1) // must not panic
}

func TestCloseIsIdempotent(t *testing.T) {
	e, _ := newRecordingEncoder(t)

	for i := range 3 {
		if err := e.Close(); err != nil {
			t.Fatalf("Close call %d: %v", i+1, err)
		}
	}
}

// TestWaitHalfBitWaits guards against the half-bit wait becoming a no-op.
func TestWaitHalfBitWaits(t *testing.T) {
	const clockHz = 100 // half-bit period: 5ms

	e, err := New(clockHz, func(Level) error { return nil })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer e.Close()

	start := time.Now()
	if !e.waitHalfBit() {
		t.Fatal("waitHalfBit reported a stopped encoder")
	}

	if elapsed := time.Since(start); elapsed < e.halfBitPeriod {
		t.Errorf("waitHalfBit returned after %v, want at least %v", elapsed, e.halfBitPeriod)
	}
}

// TestWaitHalfBitReportsShutdown covers the other return path: a stopped
// encoder must abort the wait instead of driving the remaining half-bit.
func TestWaitHalfBitReportsShutdown(t *testing.T) {
	// A half-bit period of 500ms: without the shutdown path this would block.
	e, err := New(1, func(Level) error { return nil })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	start := time.Now()
	if e.waitHalfBit() {
		t.Error("waitHalfBit completed the half-bit although the encoder was closed")
	}
	if elapsed := time.Since(start); elapsed >= e.halfBitPeriod {
		t.Errorf("waitHalfBit blocked for %v on a closed encoder, want an immediate return", elapsed)
	}
}
