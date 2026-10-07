package decoder

import "time"

// Clock recovery: how a decoder created with a bit clock of zero finds the bit
// period, follows it and settles the bit phase.
//
// The decoder buffers edges while it does not know the clock, and again while
// it knows the clock but not the phase - at the start of a transmission, and
// after an interval that matched nothing. Both are settled from the edges
// themselves:
//
//   - The clock locks once the latest lockIntervals intervals all fit a half
//     bit (T) or a full bit (2T) of one estimate, with both kinds present.
//     Manchester knows no other interval, so a window of only one kind is
//     ambiguous - a run of identical bits gives only T, alternating bits only
//     2T - and the decoder keeps waiting instead of locking on to the wrong
//     multiple.
//   - The phase is settled at the first 2T interval, which always runs from
//     one mid-bit edge to the next. Every second edge before it is a mid-bit
//     edge as well, so the buffered edges are decoded backwards from there,
//     and a preamble of identical bits is delivered in full instead of being
//     lost.
//   - Every valid interval then moves the clock a little towards its own
//     length, so the decoder follows a sender whose clock drifts.

const (
	// lockIntervals is the number of consecutive intervals the clock is
	// estimated from.
	lockIntervals = 16

	// maxPending bounds the edges buffered while the clock or the phase is
	// unknown. A longer run of identical bits drops its oldest edges, which
	// keeps the phase of the rest intact.
	maxPending = 512

	// trackingWeight is how slowly the recovered clock follows the signal:
	// each valid interval moves it by 1/trackingWeight of its deviation.
	trackingWeight = 16
)

// recoverClock buffers an edge while the clock is unknown and locks the clock
// as soon as the latest intervals allow it.
func (d *Decoder) recoverClock(event Event) {
	if event.Missed > 0 {
		// The interval to this edge spans lost edges: start over from it.
		d.pending = append(d.pending[:0], event)
		return
	}

	d.addPending(event)
	if len(d.pending) <= lockIntervals {
		return
	}

	half, ok := estimateHalfBit(d.pending[len(d.pending)-lockIntervals-1:])
	if !ok {
		return
	}

	d.setHalfBit(half)
	d.tracking = true
	d.state.Store(decodeData)
	if d.logger != nil {
		d.logger.Debug("bit clock recovered", "halfBit", half, "frequency", 1/(2*half).Seconds())
	}

	d.resolvePhase()
}

// recoverPhase buffers an edge while the clock is known but the phase is not,
// and settles the phase at the first full-bit interval.
func (d *Decoder) recoverPhase(event Event) {
	if len(d.pending) == 0 {
		d.pending = append(d.pending, event)
		return
	}

	delta := event.Time.Sub(d.pending[len(d.pending)-1].Time)
	if event.Missed > 0 || !d.fitsBitPeriod(delta) {
		d.invalidInterval(event, delta)
		return
	}

	d.invalidIntervalCount = 0
	d.addPending(event)

	if d.isFullBit(delta) {
		d.resolvePhase()
	}
}

// resolvePhase decodes the buffered edges once they contain a full-bit
// interval and hands over to regular decoding. Without one it leaves the
// phase unknown.
//
// Only the trailing edges whose intervals all fit the current clock are
// used: anything before belongs to another transmission or to noise.
func (d *Decoder) resolvePhase() {
	start := len(d.pending) - 1
	for start > 0 && d.fitsBitPeriod(d.pending[start].Time.Sub(d.pending[start-1].Time)) {
		start--
	}
	segment := d.pending[start:]

	anchor := -1 // index of the edge that opens the first full-bit interval
	for i := 1; i < len(segment); i++ {
		if d.isFullBit(segment[i].Time.Sub(segment[i-1].Time)) {
			anchor = i - 1
			break
		}
	}
	if anchor < 0 {
		d.pending = append(d.pending[:0], segment...)
		d.phaseKnown = false
		return
	}

	// The anchor is a mid-bit edge, and so is every second edge before it.
	for i := anchor % 2; i <= anchor; i += 2 {
		d.sendBit(d.decodingTable[segment[i].Edge])
	}

	// Everything after the anchor is decoded the regular way. The edges are
	// copied first, because decoding them may start a new buffer.
	rest := append([]Event(nil), segment[anchor+1:]...)
	d.pending = d.pending[:0]
	d.phaseKnown = true
	d.receivedHalfBit = 0
	d.invalidIntervalCount = 0
	d.lastTimestamp = segment[anchor].Time

	for _, event := range rest {
		d.eventHandler(event)
	}
}

// addPending appends an edge to the buffer and drops the oldest ones beyond
// maxPending.
func (d *Decoder) addPending(event Event) {
	d.pending = append(d.pending, event)
	if excess := len(d.pending) - maxPending; excess > 0 {
		d.pending = append(d.pending[:0], d.pending[excess:]...)
	}
}

// track moves the recovered clock towards the length of a valid interval.
func (d *Decoder) track(delta time.Duration, fullBit bool) {
	if fullBit {
		delta /= 2
	}
	d.setHalfBit(d.halfBitTime + (delta-d.halfBitTime)/trackingWeight)
}

// setHalfBit sets the bit periods and their tolerances from a half-bit period.
func (d *Decoder) setHalfBit(half time.Duration) {
	d.halfBitTime = half
	d.fullBitTime.Store(2 * half)
	d.halfBitTimeTolerance = half * bitTimeTolerance / 100
	d.fullBitTimeTolerance = 2 * half * bitTimeTolerance / 100
}

// isFullBit reports whether an interval matches the full bit period.
func (d *Decoder) isFullBit(delta time.Duration) bool {
	return withinTolerance(delta, d.fullBitTime.Load(), d.fullBitTimeTolerance)
}

// fitsBitPeriod reports whether an interval matches the half or the full bit
// period.
func (d *Decoder) fitsBitPeriod(delta time.Duration) bool {
	return withinTolerance(delta, d.halfBitTime, d.halfBitTimeTolerance) || d.isFullBit(delta)
}

// estimateHalfBit estimates the half-bit period from the intervals between
// consecutive edges. It reports false unless every interval fits the estimate
// as a half or a full bit and both kinds occur.
//
// The estimate starts at the shortest interval and is refined a few times:
// intervals below one and a half half-bits count as half bits, the others as
// full bits at half their length.
func estimateHalfBit(edges []Event) (time.Duration, bool) {
	if len(edges) < 2 {
		return 0, false
	}

	intervals := make([]time.Duration, len(edges)-1)
	for i := range intervals {
		intervals[i] = edges[i+1].Time.Sub(edges[i].Time)
	}

	half := intervals[0]
	for _, iv := range intervals[1:] {
		half = min(half, iv)
	}
	if half <= 0 {
		return 0, false
	}

	var halves, fulls int
	for range 3 {
		var sum time.Duration
		halves, fulls = 0, 0
		for _, iv := range intervals {
			if iv < half*3/2 {
				sum += iv
				halves++
			} else {
				sum += iv / 2
				fulls++
			}
		}
		half = sum / time.Duration(len(intervals))
	}
	if halves == 0 || fulls == 0 {
		return 0, false
	}

	tolerance := half * bitTimeTolerance / 100
	for _, iv := range intervals {
		if !withinTolerance(iv, half, tolerance) && !withinTolerance(iv, 2*half, 2*tolerance) {
			return 0, false
		}
	}

	return half, true
}
