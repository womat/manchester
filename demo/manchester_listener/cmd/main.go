// Command manchester_listener reads Manchester-encoded signals from a GPIO pin
// and prints the decoded bytes.
//
// It shows the glue the library deliberately does not provide: one goroutine
// translates gpio.Event values into decoder.Event values, a second reassembles
// the decoder's raw bit stream into bytes by looking for the start and stop
// bits the encoder frames each byte with.
//
// Flags select the GPIO line, the bit clock in Hz and the encoding convention
// (IEEE 802.3 by default, Thomas with -thomas); they must match the sender.
// Use demo/manchester_sender on the transmitting end.
//
// It runs until interrupted (e.g., via Ctrl+C).
//
//	manchester_listener -gpioline 21 -bitClock 50
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"time"

	"github.com/womat/golib/gpio"
	"github.com/womat/golib/gpio/rpi"
	"github.com/womat/golib/manchester/decoder"
)

func main() {
	gpioLine := flag.Int("gpioline", 20, "GPIO pin number")
	bitClock := flag.Int("bitClock", 50, "bit clock in Hz")
	thomas := flag.Bool("thomas", false, "use 'Differential Manchester/Thomas' encoding instead of IEEE 802.3")

	flag.Parse()

	gpioPin, err := rpi.NewPin(*gpioLine,
		rpi.WithMode(gpio.Input),
		rpi.WithPullup(gpio.PullUp),
		rpi.WithDebounce(time.Duration(0)*time.Millisecond),
	)

	if err != nil {
		log.Fatal(err)
	}
	defer gpioPin.Close()

	// Create a context to control watching lifetime
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// Watch for rising and falling edges
	gpioEvents, err := gpioPin.WatchCh(gpio.RisingEdge | gpio.FallingEdge)
	if err != nil {
		log.Fatal(err)
	}

	encoding := decoder.IEEE
	if *thomas {
		encoding = decoder.Thomas
	}

	log.Printf("GPIO Pin %d: %s", gpioPin.Number(), gpioPin.Info())
	log.Printf("Listening on GPIO Pin %d", gpioPin.Number())

	decoderEvents := make(chan decoder.Event, 1024)
	dec, err := decoder.New(decoderEvents, *bitClock,
		decoder.WithManchesterEncoding(encoding),
		// comment out the next line to disable debug logging in the decoder
		//	decoder.WithLogger(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer dec.Close()

	log.Printf("decoder info: %s", dec.Info())

	// --- Goroutine 1: GPIO → Decoder ---
	go func() {
		for {
			select {
			case evt, ok := <-gpioEvents:
				if !ok {
					return
				}
				var edge decoder.Edge
				switch evt.Edge {
				case gpio.RisingEdge:
					edge = decoder.RisingEdge
				case gpio.FallingEdge:
					edge = decoder.FallingEdge
				default:
					continue
				}
				select {
				case decoderEvents <- decoder.Event{Time: evt.Time, Edge: edge}:
				default: // Wenn Decoder voll, verwerfen
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// --- Goroutine 2: Bits → Bytes ---
	go func() {
		var b byte
		var bitCount int
		for bit := range dec.Bits() {
			if bit == decoder.Invalid {
				slog.Warn("invalid bit received, resetting")
				b = 0
				bitCount = 0
				continue
			}

			if bit != decoder.Low && bit != decoder.High {
				continue
			}

			switch bitCount {
			case 0: // Startbit = 0

				if bit == decoder.Low {
					b = 0
					bitCount++
				}
			case 9: // Stopbit = 1
				if bit == decoder.High {
					fmt.Print(string(b))
				}

				b = 0
				bitCount = 0
			default:
				b |= byte(bit) << (bitCount - 1)
				bitCount++
			}
		}
	}()

	<-ctx.Done()
	log.Printf("Decoder Info: %s", dec.Info())
	log.Println("Interrupt received, stopping...")
}
