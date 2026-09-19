package decoder

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// With a bit clock of 100Hz and the decoder's tolerance of 25%, an interval is
// accepted as a half bit between 3.75ms and 6.25ms, and as a full bit between
// 7.5ms and 12.5ms. Anything else is reported as Invalid.
const (
	testClockHz = 100
	testFullBit = 10 * time.Millisecond
	testHalfBit = 5 * time.Millisecond
	testInvalid = 20 * time.Millisecond // far above the full bit window
)

// newTestDecoder returns a decoder whose event channel stays empty, so a test
// can drive eventHandler directly and observe the result deterministically.
func newTestDecoder(t *testing.T, opts ...Option) *Decoder {
	t.Helper()

	d, err := New(make(chan Event), testClockHz, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	return d
}

// driver replays edge events with controlled spacing. It keeps its own clock
// so that several calls form one continuous signal: the decoder measures the
// interval between consecutive edges, and restarting from time.Now() on every
// call would insert a spurious zero-length interval.
type driver struct {
	d    *Decoder
	now  time.Time
	edge Edge
}

// newDriver emits the reference edge, which carries no bit itself.
func newDriver(d *Decoder) *driver {
	dr := &driver{d: d, now: time.Now(), edge: RisingEdge}
	d.eventHandler(Event{Time: dr.now, Edge: dr.edge})

	return dr
}

// intervals emits one alternating edge per given interval.
func (dr *driver) intervals(list ...time.Duration) {
	for _, interval := range list {
		dr.now = dr.now.Add(interval)
		if dr.edge == RisingEdge {
			dr.edge = FallingEdge
		} else {
			dr.edge = RisingEdge
		}
		dr.d.eventHandler(Event{Time: dr.now, Edge: dr.edge})
	}
}

// bits drains everything the decoder has emitted so far.
func bits(d *Decoder) []Bit {
	var out []Bit
	for {
		select {
		case b := <-d.c:
			out = append(out, b)
		default:
			return out
		}
	}
}

// TestEventHandlerFullBitInterval covers the unambiguous case: a full bit
// period between two edges means the current edge is a mid-bit edge, and the
// bit is decoded from its direction right away.
func TestEventHandlerFullBitInterval(t *testing.T) {
	d := newTestDecoder(t)

	// The reference edge is rising, so the next edge is falling.
	newDriver(d).intervals(testFullBit)

	got := bits(d)
	want := d.decodingTable[FallingEdge]
	if len(got) != 1 || got[0] != want {
		t.Errorf("a full bit interval produced %v, want [%v]", got, want)
	}
}

// TestEventHandlerHalfBitIntervals covers the pairing rule: a half bit period
// means an edge on the bit boundary, so only the second of two consecutive
// half bit intervals carries a bit.
func TestEventHandlerHalfBitIntervals(t *testing.T) {
	d := newTestDecoder(t)
	dr := newDriver(d)

	dr.intervals(testHalfBit)
	if got := bits(d); len(got) != 0 {
		t.Fatalf("the first half bit interval already produced %v, want nothing", got)
	}

	dr.intervals(testHalfBit)
	if got := bits(d); len(got) != 1 {
		t.Errorf("the second half bit interval produced %v, want exactly one bit", got)
	}
}

// TestEventHandlerInvalidInterval covers intervals that match neither window:
// they are reported as Invalid so the caller can drop the frame.
func TestEventHandlerInvalidInterval(t *testing.T) {
	tests := []struct {
		name     string
		interval time.Duration
	}{
		{"far too short", time.Millisecond},
		{"below the half bit window", 3 * time.Millisecond},
		{"between the two windows", 7 * time.Millisecond},
		{"above the full bit window", 15 * time.Millisecond},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newTestDecoder(t)

			newDriver(d).intervals(tt.interval)

			got := bits(d)
			if len(got) != 1 || got[0] != Invalid {
				t.Errorf("an interval of %v produced %v, want [%v]", tt.interval, got, Invalid)
			}
		})
	}
}

// TestEventHandlerIgnoresUnknownEdges makes sure a malformed event neither
// produces a bit nor disturbs the timing reference.
func TestEventHandlerIgnoresUnknownEdges(t *testing.T) {
	d := newTestDecoder(t)

	d.eventHandler(Event{Time: time.Now(), Edge: Edge(42)})

	if got := bits(d); len(got) != 0 {
		t.Errorf("an unknown edge produced %v, want nothing", got)
	}
	if !d.lastTimestamp.IsZero() {
		t.Error("an unknown edge updated the timing reference")
	}
}

// TestResynchronise covers the recovery path: after a run of unmatched
// intervals the decoder gives up on the current timing and returns to clock
// discovery instead of emitting Invalid forever.
func TestResynchronise(t *testing.T) {
	d := newTestDecoder(t)

	if got := d.state.Load(); got != decodeData {
		t.Fatalf("decoder starts in state %v, want decodeData with a known bit clock", got)
	}

	intervals := make([]time.Duration, invalidThreshold+1)
	for i := range intervals {
		intervals[i] = testInvalid
	}
	newDriver(d).intervals(intervals...)

	if got := d.resyncCount.Load(); got != 1 {
		t.Errorf("resync count is %d, want 1", got)
	}
	if got := d.state.Load(); got != discoverClock {
		t.Errorf("decoder state is %v, want discoverClock after a resync", got)
	}
	if !d.lastTimestamp.IsZero() {
		t.Error("resynchronise kept a stale timestamp, the next interval would be bogus")
	}
	if len(d.clockEventSamples) != 0 {
		t.Errorf("resynchronise kept %d clock samples, want none", len(d.clockEventSamples))
	}
}

// TestResynchroniseNeedsAWholeRun makes sure a single bad interval does not
// throw away a working clock.
func TestResynchroniseNeedsAWholeRun(t *testing.T) {
	d := newTestDecoder(t)

	// One bad interval, then a good one, repeated well past the threshold.
	dr := newDriver(d)
	for i := 0; i < invalidThreshold*2; i++ {
		dr.intervals(testInvalid, testFullBit)
	}

	if got := d.resyncCount.Load(); got != 0 {
		t.Errorf("resync count is %d, want 0: a good interval must reset the run", got)
	}
}

// TestSendBitCountsBufferOverflows covers the documented behaviour of a
// consumer that is too slow: bits are dropped rather than blocking the
// decoder, and the loss is counted.
func TestSendBitCountsBufferOverflows(t *testing.T) {
	const bufferSize = 2

	d := newTestDecoder(t, WithBufferSize(bufferSize))

	const sent = 5
	for i := 0; i < sent; i++ {
		d.sendBit(High)
	}

	if got, want := d.bufferOverflowCount.Load(), uint64(sent-bufferSize); got != want {
		t.Errorf("buffer overflow count is %d, want %d", got, want)
	}
	if got := len(bits(d)); got != bufferSize {
		t.Errorf("%d bits are readable, want %d", got, bufferSize)
	}
}

func TestWithLogger(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	d := newTestDecoder(t, WithLogger(logger))
	newDriver(d).intervals(testFullBit)

	if !strings.Contains(buf.String(), "full bit detected") {
		t.Errorf("the decoder logged %q, want it to report the decoded bit", buf.String())
	}
}

// TestWithoutLoggerIsSilent covers the default: no logger means no output and,
// more importantly, no nil dereference on the hot path.
func TestWithoutLoggerIsSilent(t *testing.T) {
	d := newTestDecoder(t)

	newDriver(d).intervals(testFullBit, testHalfBit, testHalfBit, testInvalid)

	if d.logger != nil {
		t.Error("a decoder without WithLogger has a logger installed")
	}
}

func TestInfoReportsTheDecoderState(t *testing.T) {
	d := newTestDecoder(t)

	info := d.Info()
	if !strings.Contains(info, "decoding data") {
		t.Errorf("Info() = %q, want it to report the decoding state", info)
	}
	if !strings.Contains(info, "100.00 Hz") {
		t.Errorf("Info() = %q, want it to report the configured bit clock", info)
	}

	discovering, err := New(make(chan Event), 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer discovering.Close()

	if info := discovering.Info(); !strings.Contains(info, "discovering clock") {
		t.Errorf("Info() = %q, want it to report the discovery state", info)
	}
}
