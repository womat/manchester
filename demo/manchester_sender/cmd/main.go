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
	freq := flag.Int("freq", 50, "Carrier frequency in Hzs")
	msb := flag.Bool("msb", false, "Use MSB instead of LSB")
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

	setValue := func(level int) error {
		return gpioPin.SetValue(gpio.Level(level))
	}

	order := encoder.LSBFirst
	if *msb {
		order = encoder.MSBFirst
	}

	enc := encoder.New(uint(*freq), setValue,
		encoder.WithBitOrder(order),
		encoder.WithSyncBytes(2), // optional: Anzahl Sync-Bytes
	)
	defer enc.Close()
	_, err = enc.Write(msg)
	if err != nil {
		log.Fatal(err)
	}

	// Warten, bis alles gesendet wurde
	enc.Flush()
	log.Println("Message sent!")
}
