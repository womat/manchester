// Command manchester_bench measures Manchester transmissions over a GPIO path
// on one Raspberry Pi: one process sends known frames on an output, a second
// one receives them on an input looped back to it (directly or through an
// optocoupler) and counts how many arrive intact.
//
//	manchester_bench recv -in 25 -rate 1000 -n 50 -t 40s [-thomas] [-fixed]
//	manchester_bench send -out 18 -rate 1000 -n 50
//	manchester_bench spisend -rate 1000 -n 50 [-dev /dev/spidev0.0] [-k 10]
//
// Start recv first, then send with the same -rate, -n and -len. Frame i
// starts with '#' and i, the rest is pseudo-random from a fixed seed, so the
// receiver knows every byte it should get. Each frame is preceded by two sync
// bytes; bytes are framed with a start and a stop bit like the encoder does.
//
// send drives the line from Go through the encoder, so scheduler and garbage
// collector pauses end up in the signal. spisend clocks the same half bits out
// of the SPI controller (MOSI, GPIO 10) instead, which is jitter-free; it needs
// SPI enabled (sudo dtparam spi=on).
//
// recv prints the intact frames, the deviation of each edge interval from the
// nominal half or full bit as a fraction of a half bit (median, p99, p99.9,
// max), and how many edges were dropped in gpio or in the hand-over to the
// decoder. An inverting path such as an optocoupler needs -thomas.
//
// series.sh runs the whole series (three paths × five bit rates) on the bench
// described in github.com/womat/gpio/demo/wirecheck.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"time"

	"github.com/womat/gpio"
	"github.com/womat/gpio/rpi"
	"github.com/womat/manchester/decoder"
	"github.com/womat/manchester/encoder"
)

// payload returns the fixed content of frame i.
func payload(i, n int) []byte {
	b := make([]byte, n)
	b[0], b[1] = '#', byte(i)
	r := rand.New(rand.NewPCG(uint64(i), 7))
	for k := 2; k < n; k++ {
		b[k] = byte(r.Uint32())
	}
	return b
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func main() {
	if len(os.Args) < 2 {
		fail(errors.New("usage: manchester_bench send|spisend|recv [flags]"))
	}
	switch os.Args[1] {
	case "send":
		send(os.Args[2:])
	case "spisend":
		spiSend(os.Args[2:])
	case "recv":
		recv(os.Args[2:])
	default:
		fail(fmt.Errorf("unknown command %q", os.Args[1]))
	}
}

func send(args []string) {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	out := fs.Int("out", 18, "output GPIO")
	rate := fs.Int("rate", 1000, "bit/s")
	n := fs.Int("n", 50, "frames")
	size := fs.Int("len", 56, "bytes per frame")
	gap := fs.Duration("gap", 50*time.Millisecond, "pause between frames")
	delay := fs.Duration("delay", time.Second, "wait before the first frame")
	_ = fs.Parse(args)

	p, err := rpi.NewPin(*out, rpi.WithMode(gpio.Output))
	if err != nil {
		fail(err)
	}
	defer p.Close()
	errs := 0
	enc, err := encoder.New(*rate, func(l encoder.Level) error { return p.SetValue(gpio.Level(l)) },
		encoder.WithSyncBytes(2), encoder.WithErrorHandler(func(error) { errs++ }))
	if err != nil {
		fail(err)
	}
	defer enc.Close()

	time.Sleep(*delay)
	start := time.Now()
	for i := range *n {
		if _, err := enc.Send(payload(i, *size)); err != nil {
			fail(err)
		}
		enc.Wait()
		time.Sleep(*gap)
	}
	fmt.Printf("send: %d frames in %v, SetValue errors %d\n", *n, time.Since(start).Round(time.Millisecond), errs)
}

func recv(args []string) {
	fs := flag.NewFlagSet("recv", flag.ExitOnError)
	in := fs.Int("in", 25, "input GPIO")
	rate := fs.Int("rate", 1000, "bit/s (nominal, for the statistics)")
	fixed := fs.Bool("fixed", false, "configure the bit clock instead of recovering it")
	thomas := fs.Bool("thomas", false, "Thomas convention (inverting path)")
	n := fs.Int("n", 50, "expected frames")
	size := fs.Int("len", 56, "bytes per frame")
	dur := fs.Duration("t", 40*time.Second, "how long to receive")
	_ = fs.Parse(args)

	p, err := rpi.NewPin(*in, rpi.WithMode(gpio.Input), rpi.WithPullup(gpio.PullUp))
	if err != nil {
		fail(err)
	}
	defer p.Close()
	gpioEvents, err := p.WatchCh(gpio.RisingEdge | gpio.FallingEdge)
	if err != nil {
		fail(err)
	}

	enc := decoder.IEEE
	if *thomas {
		enc = decoder.Thomas
	}
	clock := 0
	if *fixed {
		clock = *rate
	}
	decEvents := make(chan decoder.Event, 1024)
	dec, err := decoder.New(decEvents, clock, decoder.WithManchesterEncoding(enc))
	if err != nil {
		fail(err)
	}
	defer dec.Close()

	half := time.Second / time.Duration(2**rate)
	var halfErr []float64 // deviation per interval, as a fraction of a half bit
	var glueDropped uint64
	glueDone := make(chan struct{})

	// Edges → decoder, as in demo/manchester_listener, plus the half-bit error.
	go func() {
		defer close(glueDone)
		var missed uint64
		var last time.Time
		for evt := range gpioEvents {
			if !last.IsZero() && evt.Missed == 0 {
				d := float64(evt.Time.Sub(last)) / float64(half)
				switch {
				case d >= 0.5 && d < 1.5:
					halfErr = append(halfErr, abs(d-1))
				case d >= 1.5 && d < 2.5:
					halfErr = append(halfErr, abs(d-2))
				}
			}
			last = evt.Time
			edge := decoder.FallingEdge
			if evt.Edge == gpio.RisingEdge {
				edge = decoder.RisingEdge
			}
			select {
			case decEvents <- decoder.Event{Time: evt.Time, Edge: edge, Missed: evt.Missed + missed}:
				missed = 0
			default:
				missed += evt.Missed + 1
				glueDropped++
			}
		}
	}()

	// Bits → bytes → frames.
	ok := map[int]bool{}
	bad, invalid := 0, 0
	var cur []byte
	finish := func() {
		if len(cur) >= 2 && cur[0] == '#' {
			if i := int(cur[1]); len(cur) >= *size && bytes.Equal(cur[:*size], payload(i, *size)) {
				ok[i] = true
			} else if len(cur) > 4 {
				bad++
			}
		} else if len(cur) > 4 {
			bad++
		}
		cur = cur[:0]
	}
	bitsDone := make(chan struct{})
	go func() {
		defer close(bitsDone)
		var b byte
		count := 0
		for bit := range dec.Bits() {
			if bit == decoder.Invalid {
				invalid++
				finish()
				b, count = 0, 0
				continue
			}
			switch count {
			case 0:
				if bit == decoder.Low {
					b, count = 0, 1
				}
			case 9:
				if bit == decoder.High {
					cur = append(cur, b)
					if len(cur) == *size {
						finish()
					}
				} else {
					finish() // framing error
				}
				b, count = 0, 0
			default:
				b |= byte(bit) << (count - 1)
				count++
			}
		}
		finish()
	}()

	time.Sleep(*dur)
	info := dec.Info()
	_ = p.Close()
	<-glueDone
	close(decEvents)
	_ = dec.Close()
	<-bitsDone

	slices.Sort(halfErr)
	q := func(f float64) float64 {
		if len(halfErr) == 0 {
			return 0
		}
		return halfErr[int(f*float64(len(halfErr)-1))] * 100
	}
	fmt.Printf("recv: %d/%d frames intact, %d corrupt, invalid bits %d\n", len(ok), *n, bad, invalid)
	fmt.Printf("      half-bit error: median %.1f %%  p99 %.1f %%  p99.9 %.1f %%  max %.1f %%  (n=%d)\n",
		q(0.5), q(0.99), q(0.999), q(1), len(halfErr))
	fmt.Printf("      gpio dropped %d, glue dropped %d; %s\n", p.DroppedEvents(), glueDropped, info)
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
