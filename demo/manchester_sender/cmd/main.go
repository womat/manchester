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
	"flag"
	"log"
	"log/slog"

	"github.com/womat/golib/gpio"
	"github.com/womat/golib/gpio/rpi"
	"github.com/womat/golib/manchester/encoder"
)

func main() {
	gpioLine := flag.Int("gpioline", 21, "GPIO pin number")
	bitClock := flag.Int("bitClock", 50, "bit clock in Hz")
	msb := flag.Bool("msb", false, "Use MSB instead of LSB")
	thomas := flag.Bool("thomas", false, "use 'Differential Manchester/Thomas' encoding instead of IEEE 802.3")
	sync := flag.Int("sync", 1, "number of sync bytes (0xff) to send before the message")
	flag.Parse()

	if flag.NArg() == 0 {
		log.Fatal("No message provided")
	}
	msg := []byte(flag.Arg(0))

	gpioPin, err := rpi.NewPin(*gpioLine,
		rpi.WithMode(gpio.Output))

	if err != nil {
		log.Fatal(err)
	}
	defer gpioPin.Close()

	log.Printf("GPIO Pin %d: %s", gpioPin.Number(), gpioPin.Info())

	setValue := func(level encoder.Level) error {
		return gpioPin.SetValue(gpio.Level(level))
	}

	order := encoder.LSBFirst
	if *msb {
		order = encoder.MSBFirst
	}
	encoding := encoder.IEEE
	if *thomas {
		encoding = encoder.Thomas
	}

	enc := encoder.New(*bitClock, setValue,
		encoder.WithBitOrder(order),
		encoder.WithSyncBytes(*sync),
		encoder.WithManchesterEncoding(encoding),
		// comment out the next line to disable debug logging in the encoder
		encoder.WithErrorHandler(func(err error) { slog.Error("encoder GPIO error", "error", err) }),
	)

	defer enc.Close()
	_, err = enc.Send(msg)
	if err != nil {
		log.Fatal(err)
	}

	// Warten, bis alles gesendet wurde
	enc.Wait()
	log.Println("Message sent!")
}
