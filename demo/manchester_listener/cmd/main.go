// This program reads Manchester-encoded signals from the GPIO pin and outputs the decoded bytes.
// The watcher runs until interrupted (e.g., via Ctrl+C).
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
	gpioEvents, err := gpioPin.WatchCh(ctx, gpio.RisingEdge|gpio.FallingEdge)
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
	dec := decoder.New(decoderEvents, *bitClock,
		decoder.WithManchesterEncoding(encoding),
		// comment out the next line to disable debug logging in the decoder
		decoder.WithLogger(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))),
	)
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
		for {
			select {
			case bit, ok := <-dec.C:
				if !ok {
					return
				}
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
			case <-ctx.Done():
				return
			}
		}
	}()
	<-ctx.Done()
	log.Printf("Encoder Info: %s", dec.Info())
	log.Println("Interrupt received, stopping...")
}
