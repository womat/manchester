// Command manchester_sender sends one Manchester-encoded message over a GPIO
// pin and exits.
//
// The message is the single positional argument. Flags select the GPIO line,
// the bit clock in Hz, the bit order (LSB first by default), the number of
// 0xff sync bytes sent ahead of the data, and the encoding convention
// (IEEE 802.3 by default, Thomas with -thomas). The pin is configured as an
// output; a transmission error is reported through the encoder's error
// handler, since the encoder transmits from a background goroutine.
//
// The program blocks in enc.Wait() until the message has been transmitted, so
// it terminates on its own. Use demo/manchester_listener on the receiving end.
//
//	manchester_sender -gpioline 21 -bitClock 50 "Hello World"
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

	enc, err := encoder.New(*bitClock, setValue,
		encoder.WithBitOrder(order),
		encoder.WithSyncBytes(*sync),
		encoder.WithManchesterEncoding(encoding),
		// comment out the next line to disable debug logging in the encoder
		encoder.WithErrorHandler(func(err error) { slog.Error("encoder GPIO error", "error", err) }),
	)

	if err != nil {
		log.Fatal(err)
	}
	defer enc.Close()

	if _, err = enc.Send(msg); err != nil {
		log.Fatal(err)
	}

	// Warten, bis alles gesendet wurde
	enc.Wait()
	log.Println("Message sent!")
}
