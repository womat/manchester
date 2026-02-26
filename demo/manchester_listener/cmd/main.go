// Command gpio-watch is a CLI tool for monitoring GPIO input events on a Raspberry Pi.
//
// It allows selecting a GPIO pin, configuring rising and/or falling edge detection,
// and optionally setting a debounce time in milliseconds. The program initializes
// the pin as input with an internal pull-up resistor and logs each detected edge
// event with a precise timestamp.
//
// The watcher runs until interrupted (e.g., via Ctrl+C).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"time"

	"github.com/womat/golib/gpio"
	"github.com/womat/golib/gpio/rpi"
	"github.com/womat/golib/manchester/decoder"
)

func main() {
	gpioLine := flag.Int("gpioline", 21, "GPIO pin number")

	flag.Parse()

	gpioPin, err := rpi.NewPin(*gpioLine)
	if err != nil {
		log.Fatal(err)
	}
	defer gpioPin.Close()

	if err = gpioPin.SetMode(gpio.Input); err != nil {
		log.Fatal(err)
	}
	if err = gpioPin.SetPullMode(gpio.PullUp); err != nil {
		log.Fatal(err)
	}
	if err = gpioPin.SetDebounce(time.Duration(0) * time.Millisecond); err != nil {
		log.Fatal(err)
	}

	// Create a context to control watching lifetime
	// --- Kontext für sauberes Stoppen ---
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// Watch for rising and falling edges
	gpioEvents, err := gpioPin.Watch(ctx, gpio.RisingEdge|gpio.FallingEdge)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("GPIO Pin %d: %s", gpioPin.Number(), gpioPin.Info())
	log.Printf("Listening on GPIO Pin %d", gpioPin.Number())

	decoderEvents := make(chan decoder.Event, 1024)
	dec := decoder.New(decoderEvents)
	defer dec.Close()

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
						fmt.Print(b)
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

	// Warten bis Interrupt
	<-ctx.Done()
	log.Println("Interrupt received, stopping...")
	gpioPin.Close()
	dec.Close()
}
