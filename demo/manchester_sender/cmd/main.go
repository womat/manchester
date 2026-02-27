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

	"github.com/womat/golib/gpio"
	"github.com/womat/golib/gpio/rpi"
	"github.com/womat/golib/manchester/encoder"
)

func main() {
	gpioLine := flag.Int("gpioline", 21, "GPIO pin number")
	bitClock := flag.Int("bitClock", 50, "bit clock in Hz")
	msb := flag.Bool("msb", false, "Use MSB instead of LSB")
	thomas := flag.Bool("thomas", false, "use 'Differential Manchester/Thomas' encoding instead of IEEE 802.3")
	sync := flag.Int("sync", 0, "number of sync bytes to send before the message")
	flag.Parse()

	if flag.NArg() == 0 {
		log.Fatal("No message provided")
	}
	msg := []byte(flag.Arg(0))

	gpioPin, err := rpi.NewPin(*gpioLine)
	if err != nil {
		log.Fatal(err)
	}
	defer gpioPin.Close()

	if err = gpioPin.SetMode(gpio.Output); err != nil {
		log.Fatal(err)
	}

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
	)
	defer enc.Close()
	_, err = enc.Write(msg)
	if err != nil {
		log.Fatal(err)
	}

	// Warten, bis alles gesendet wurde
	enc.Wait()
	log.Println("Message sent!")
}
