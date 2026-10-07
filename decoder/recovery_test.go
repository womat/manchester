package decoder

import (
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"
)

// signal produces the edges a Manchester sender puts on the line, with a
// controllable length for every half-bit.
type signal struct {
	table [2][2]int // [bit][half] line level, the inverse of decodingTable
	half  func(i int) time.Duration
	now   time.Time
	level int // current line level; the line idles low
	i     int // half-bits driven so far

	events []Event
}

func newSignal(enc ManchesterEncoding, half func(i int) time.Duration) *signal {
	table := [2][2]int{Low: {1, 0}, High: {0, 1}} // IEEE: a 1 rises at mid-bit
	if enc == Thomas {
		table = [2][2]int{Low: {0, 1}, High: {1, 0}}
	}
	return &signal{table: table, half: half, now: time.Now()}
}

// constant returns a half-bit length function for a fixed bit clock.
func constant(hz float64) func(int) time.Duration {
	half := time.Duration(float64(time.Second) / hz / 2)
	return func(int) time.Duration { return half }
}

// send drives the two half-bits of every bit.
func (s *signal) send(bits ...Bit) *signal {
	for _, b := range bits {
		s.drive(s.table[b][0])
		s.drive(s.table[b][1])
	}
	return s
}

// pause holds the line for d, as between two transmissions.
func (s *signal) pause(d time.Duration) *signal {
	s.now = s.now.Add(d)
	return s
}

func (s *signal) drive(level int) {
	if level != s.level {
		edge := FallingEdge
		if level == 1 {
			edge = RisingEdge
		}
		s.events = append(s.events, Event{Time: s.now, Edge: edge})
		s.level = level
	}
	s.now = s.now.Add(s.half(s.i))
	s.i++
}

// frame returns a preamble of 16 ones, a low start bit and n pseudo-random
// bits - the shape of a DL-Bus frame or of the encoder's sync bytes.
func frame(seed uint64, n int) []Bit {
	r := rand.New(rand.NewPCG(seed, seed))
	bits := make([]Bit, 0, 17+n)
	for range 16 {
		bits = append(bits, High)
	}
	bits = append(bits, Low)
	for range n {
		bits = append(bits, Bit(r.IntN(2)))
	}
	return bits
}

// decode runs edges through a decoder without a bit clock.
func decode(t *testing.T, enc ManchesterEncoding, events []Event) (*Decoder, []Bit) {
	t.Helper()

	d, err := New(make(chan Event), 0, WithManchesterEncoding(enc), WithBufferSize(8192))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	for _, e := range events {
		d.eventHandler(e)
	}
	return d, bits(d)
}

// TestRecoveryDecodesAnyBitRate is the reason clock recovery exists: the same
// decoder reads senders of any speed without being told the bit clock, and
// delivers the preamble in full instead of losing its beginning.
func TestRecoveryDecodesAnyBitRate(t *testing.T) {
	for _, hz := range []float64{50, 488, 1000, 9600} {
		for _, enc := range []ManchesterEncoding{IEEE, Thomas} {
			want := frame(uint64(hz), 200)
			s := newSignal(enc, constant(hz)).send(want...)

			_, got := decode(t, enc, s.events)

			if !slices.Equal(got, want) {
				t.Errorf("%v Hz, encoding %v: decoded %d bits, want the %d sent\n got %v\nwant %v",
					hz, enc, len(got), len(want), got, want)
			}
		}
	}
}

// TestRecoveryFollowsAnInexactSender covers a third-party sender: its clock is
// off the nominal rate, drifts during the transmission and jitters on every
// half-bit.
func TestRecoveryFollowsAnInexactSender(t *testing.T) {
	const (
		nominal = 488.0
		bits    = 2000
	)
	base := float64(time.Second) / nominal / 2
	r := rand.New(rand.NewPCG(1, 2))

	tests := []struct {
		name string
		half func(i int) time.Duration
	}{
		{"5% slow", func(int) time.Duration { return time.Duration(base * 1.05) }},
		{"5% fast", func(int) time.Duration { return time.Duration(base * 0.95) }},
		{"drifting 10% over the frame", func(i int) time.Duration {
			return time.Duration(base * (1 + 0.10*float64(i)/(2*bits)))
		}},
		{"5% jitter", func(int) time.Duration {
			return time.Duration(base * (0.95 + 0.10*r.Float64()))
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := frame(7, bits)
			s := newSignal(IEEE, tt.half).send(want...)

			d, got := decode(t, IEEE, s.events)

			if !slices.Equal(got, want) {
				t.Errorf("decoded %d bits with %d differences, want the %d sent",
					len(got), diff(got, want), len(want))
			}
			if n := d.resyncCount.Load(); n != 0 {
				t.Errorf("resync count is %d, want 0", n)
			}
		})
	}
}

// diff counts the positions at which two bit streams differ, plus the
// difference in length.
func diff(a, b []Bit) int {
	n := max(len(a), len(b)) - min(len(a), len(b))
	for i := range min(len(a), len(b)) {
		if a[i] != b[i] {
			n++
		}
	}
	return n
}

// TestRecoveryDoesNotLockOnAlternatingBits is the regression test for the old
// clock discovery: alternating bits have a transition on every mid-bit only,
// so every interval is a full bit, and it locked on to twice the real period.
// Clock recovery waits for half-bit intervals, and then decodes the
// alternating bits it buffered meanwhile as well.
//
// The receiver starts listening after the first edge: from the idle line, the
// first bit would otherwise contribute a half-bit interval.
func TestRecoveryDoesNotLockOnAlternatingBits(t *testing.T) {
	alternating := make([]Bit, 100)
	for i := range alternating {
		alternating[i] = Bit(i % 2)
	}

	s := newSignal(IEEE, constant(488)).send(alternating...)
	d, got := decode(t, IEEE, s.events[1:])

	if len(got) != 0 {
		t.Fatalf("alternating bits alone produced %d bits, want none before a half-bit interval", len(got))
	}
	if st := d.state.Load(); st != discoverClock {
		t.Fatalf("decoder state is %v after alternating bits, want discoverClock", st)
	}

	rest := frame(3, 50)
	mark := len(s.events)
	s.send(rest...)
	for _, e := range s.events[mark:] {
		d.eventHandler(e)
	}

	want := append(alternating, rest...)
	if got := bits(d); !slices.Equal(got, want) {
		t.Errorf("decoded %d bits with %d differences, want all %d sent", len(got), diff(got, want), len(want))
	}
}

// TestRecoveryNeedsBothBitPeriods covers a run of identical bits: every
// interval is a half bit, which could just as well be a full bit of a clock
// twice as slow, so the decoder must not lock.
func TestRecoveryNeedsBothBitPeriods(t *testing.T) {
	ones := make([]Bit, 64)
	for i := range ones {
		ones[i] = High
	}

	d, got := decode(t, IEEE, newSignal(IEEE, constant(50)).send(ones...).events)

	if len(got) != 0 {
		t.Errorf("a run of identical bits produced %v, want nothing", got)
	}
	if st := d.state.Load(); st != discoverClock {
		t.Errorf("decoder state is %v, want discoverClock", st)
	}
}

// TestRecoveryAcrossAPause covers frames separated by idle time: the pause is
// one invalid interval, the clock is kept, and the next frame is decoded in
// full once its phase is settled again.
func TestRecoveryAcrossAPause(t *testing.T) {
	first, second := frame(1, 80), frame(2, 80)
	s := newSignal(IEEE, constant(488)).send(first...).pause(100 * time.Millisecond).send(second...)

	d, got := decode(t, IEEE, s.events)

	want := append(append(slices.Clone(first), Invalid), second...)
	if !slices.Equal(got, want) {
		t.Errorf("decoded %d bits with %d differences, want both frames around one Invalid",
			len(got), diff(got, want))
	}
	if n := d.resyncCount.Load(); n != 0 {
		t.Errorf("resync count is %d, want 0: a pause must not discard the clock", n)
	}
}

// TestRecoveryAfterLostEdges covers an interval across lost edges: it is
// reported as Invalid, and decoding continues once the phase is settled again.
func TestRecoveryAfterLostEdges(t *testing.T) {
	want := frame(5, 300)
	s := newSignal(IEEE, constant(488)).send(want...)

	events := slices.Clone(s.events)
	events[len(events)/2].Missed = 1

	_, got := decode(t, IEEE, events)

	if !slices.Contains(got, Invalid) {
		t.Fatal("an interval across lost edges was not reported as Invalid")
	}
	const tail = 100
	if !slices.Equal(got[len(got)-tail:], want[len(want)-tail:]) {
		t.Errorf("the last %d bits after the gap differ from the ones sent", tail)
	}
}

// TestRecoveryResynchronises covers a sender that changes its bit rate: the
// old clock produces a run of invalid intervals, the decoder discards it and
// recovers the new one.
func TestRecoveryResynchronises(t *testing.T) {
	s := newSignal(IEEE, constant(1000)).send(frame(1, 100)...)
	s.half = constant(50)
	second := frame(2, 100)
	s.send(second...)

	d, got := decode(t, IEEE, s.events)

	if n := d.resyncCount.Load(); n == 0 {
		t.Fatal("resync count is 0, want a resync after the rate change")
	}
	// The first intervals of the new frame are spent on the resync.
	const tail = 50
	if !slices.Equal(got[len(got)-tail:], second[len(second)-tail:]) {
		t.Errorf("the last %d bits after the rate change differ from the ones sent", tail)
	}
	if info := d.Info(); !strings.Contains(info, "50.00 Hz") {
		t.Errorf("Info() = %q, want the new clock of 50 Hz", info)
	}
}

// TestRecoveryReportsTheClock makes sure Info() shows the recovered clock.
func TestRecoveryReportsTheClock(t *testing.T) {
	d, _ := decode(t, IEEE, newSignal(IEEE, constant(488)).send(frame(9, 50)...).events)

	info := d.Info()
	if !strings.Contains(info, "decoding data") || !strings.Contains(info, "488.00 Hz") {
		t.Errorf("Info() = %q, want it to report decoding at 488 Hz", info)
	}
}

func TestEstimateHalfBit(t *testing.T) {
	const half = 10 * time.Millisecond

	edges := func(intervals ...time.Duration) []Event {
		now := time.Now()
		out := []Event{{Time: now}}
		for _, iv := range intervals {
			now = now.Add(iv)
			out = append(out, Event{Time: now})
		}
		return out
	}
	h, f := half, 2*half

	tests := []struct {
		name   string
		edges  []Event
		want   time.Duration
		wantOK bool
	}{
		{"half and full bits", edges(h, h, f, h, h, f, f, h, h), half, true},
		{"5% off both ways", edges(h*95/100, h*105/100, f*95/100, f*105/100), half, true},
		{"only half bits", edges(h, h, h, h, h, h), 0, false},
		{"only full bits", edges(f, f, f, f, f, f), 0, false},
		{"an interval that fits neither", edges(h, f, h, h, 3*h), 0, false},
		{"a glitch", edges(h, f, h, h/5, h), 0, false},
		{"no interval", edges(), 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := estimateHalfBit(tt.edges)
			if ok != tt.wantOK {
				t.Fatalf("estimateHalfBit reported %v, want %v", ok, tt.wantOK)
			}
			if ok && !withinTolerance(got, tt.want, tt.want/100) {
				t.Errorf("estimated half bit %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDecodingTable(t *testing.T) {
	tests := []struct {
		name     string
		encoding ManchesterEncoding
		rising   Bit
		falling  Bit
	}{
		// IEEE 802.3: a logical 1 is a low-to-high transition at mid-bit.
		{"IEEE", IEEE, High, Low},
		// G.E. Thomas uses the opposite convention.
		{"Thomas", Thomas, Low, High},
		// Unsupported values are rejected by New(), the table falls back to IEEE.
		{"unknown falls back to IEEE", ManchesterEncoding(42), High, Low},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			table := decodingTable(tt.encoding)

			if got := table[RisingEdge]; got != tt.rising {
				t.Errorf("rising mid-bit edge decodes to %v, want %v", got, tt.rising)
			}
			if got := table[FallingEdge]; got != tt.falling {
				t.Errorf("falling mid-bit edge decodes to %v, want %v", got, tt.falling)
			}
		})
	}
}
