package decoder

import (
	"math/rand"
	"testing"
	"time"
)

func TestMedian(t *testing.T) {
	tests := []struct {
		name     string
		sorted   []time.Duration
		expected time.Duration
	}{
		{"empty", nil, 0},
		{"single element", []time.Duration{7 * time.Millisecond}, 7 * time.Millisecond},
		{"odd length returns the middle element",
			[]time.Duration{1, 2, 30, 40, 50}, 30},
		{"even length averages the two middle elements",
			[]time.Duration{1, 2, 10, 20}, 6},
		{"even length rounds towards zero",
			[]time.Duration{1, 2, 3, 4}, 2}, // (2+3)/2 = 2.5 -> 2
		{"all elements equal",
			[]time.Duration{5, 5, 5, 5, 5}, 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := median(tt.sorted); got != tt.expected {
				t.Errorf("median(%v) = %v, want %v", tt.sorted, got, tt.expected)
			}
		})
	}
}

// samples builds a shuffled set of edge intervals: half-bit intervals occur
// between the two halves of a bit, full-bit intervals between the mid-bit
// edges of two consecutive equal bits.
func samples(t *testing.T, halfCount int, half time.Duration, fullCount int, full time.Duration, jitterPercent int) []time.Duration {
	t.Helper()

	r := rand.New(rand.NewSource(1)) // fixed seed: the test must not flake
	jitter := func(d time.Duration) time.Duration {
		if jitterPercent == 0 {
			return d
		}
		span := int64(d) * int64(jitterPercent) / 100
		return d + time.Duration(r.Int63n(2*span+1)-span)
	}

	out := make([]time.Duration, 0, halfCount+fullCount)
	for i := 0; i < halfCount; i++ {
		out = append(out, jitter(half))
	}
	for i := 0; i < fullCount; i++ {
		out = append(out, jitter(full))
	}
	r.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })

	return out
}

func TestCalcBitPeriodsNeedsEnoughSamples(t *testing.T) {
	tests := []struct {
		name  string
		count int
		valid bool
	}{
		{"no samples", 0, false},
		{"one below the minimum", clockEventSamples/2 - 1, false},
		{"exactly the minimum", clockEventSamples / 2, true},
		{"a full set", clockEventSamples, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Two thirds half-bit intervals, one third full-bit intervals.
			in := samples(t, tt.count*2/3, 10*time.Millisecond, tt.count-tt.count*2/3, 20*time.Millisecond, 0)

			half, full := calcBitPeriods(in)

			if tt.valid {
				if half <= 0 || full <= 0 {
					t.Fatalf("calcBitPeriods returned (%v, %v) for %d samples, want usable periods", half, full, tt.count)
				}
				return
			}
			if half != 0 || full != 0 {
				t.Errorf("calcBitPeriods returned (%v, %v) for %d samples, want (0, 0)", half, full, tt.count)
			}
		})
	}
}

func TestCalcBitPeriods(t *testing.T) {
	const (
		half = 10 * time.Millisecond
		full = 20 * time.Millisecond
	)

	tests := []struct {
		name   string
		half   int
		full   int
		jitter int
	}{
		{"clean signal", 300, 200, 0},
		{"mostly half-bit intervals", 400, 100, 0},
		{"mostly full-bit intervals", 150, 350, 0},
		{"5 percent jitter", 300, 200, 5},
		{"15 percent jitter", 300, 200, 15},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := samples(t, tt.half, half, tt.full, full, tt.jitter)

			gotHalf, gotFull := calcBitPeriods(in)

			// The decoder accepts edges within bitTimeTolerance of the period,
			// so a discovered period is only useful within that same band.
			assertNear(t, "half bit period", gotHalf, half)
			assertNear(t, "full bit period", gotFull, full)
		})
	}
}

// assertNear checks that got is within bitTimeTolerance percent of want, which
// is the band the decoder itself applies when matching intervals.
func assertNear(t *testing.T, what string, got, want time.Duration) {
	t.Helper()

	tolerance := want * bitTimeTolerance / 100
	if d := got - want; d < -tolerance || d > tolerance {
		t.Errorf("%s = %v, want %v +/- %v", what, got, want, tolerance)
	}
}

// TestCalcBitPeriodsReordersInput documents that the input slice is sorted in
// place. The decoder relies on it being disposable and truncates the slice
// afterwards; a caller that keeps its own order would be surprised.
func TestCalcBitPeriodsReordersInput(t *testing.T) {
	in := samples(t, 300, 10*time.Millisecond, 200, 20*time.Millisecond, 0)

	calcBitPeriods(in)

	for i := 1; i < len(in); i++ {
		if in[i-1] > in[i] {
			t.Fatalf("input is not sorted at index %d: %v > %v", i, in[i-1], in[i])
		}
	}
}

// TestCalcBitPeriodsCannotLockOnAlternatingBits records a limitation of the
// discovery heuristic rather than desired behaviour.
//
// An alternating bit pattern (0x55, 0xaa, ...) has a mid-bit transition in
// every bit and no transition at all on the bit boundaries, so every interval
// is a full-bit one. With no short intervals to compare against, the split
// between half and full is never found and the fallback reports the full bit
// period as the half bit period - and twice the full period as the full one.
//
// Clock discovery therefore needs a signal that contains runs of equal bits;
// the sync preamble of 0xff bytes provides exactly that. If the heuristic is
// ever improved, this test is expected to fail and should be updated.
func TestCalcBitPeriodsCannotLockOnAlternatingBits(t *testing.T) {
	const full = 20 * time.Millisecond

	in := samples(t, 0, 0, clockEventSamples, full, 0)

	half, gotFull := calcBitPeriods(in)

	if half != full {
		t.Errorf("half bit period = %v, want %v (the limitation described above)", half, full)
	}
	if gotFull != 2*full {
		t.Errorf("full bit period = %v, want %v (the limitation described above)", gotFull, 2*full)
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
