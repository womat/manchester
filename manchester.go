// Package manchester is the entry point of the Manchester line coding module;
// the code lives in its two subpackages.
//
// github.com/womat/manchester/encoder turns bytes into a sequence of line
// levels, written through a SetValue callback. github.com/womat/manchester/decoder
// turns a channel of edge events back into bits, recovering the sender's bit
// clock from the signal unless one is configured. Both support IEEE 802.3 and
// G.E. Thomas (differential) conventions; sender and receiver must agree.
//
// Neither package imports the other, nor any GPIO package: the application
// provides the glue, which keeps the codec usable over any transport.
//
// # Example usage
//
//	// Sender: drive a pin through the encoder's callback.
//	enc, err := encoder.New(50,
//	    func(level encoder.Level) error { return pin.SetValue(gpio.Level(level)) },
//	)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer enc.Close()
//
//	if _, err := enc.Send([]byte("hello")); err != nil {
//	    log.Fatal(err)
//	}
//	enc.Wait() // Send is asynchronous
//
//	// Receiver: feed edge events to the decoder and read bits. A bit
//	// clock of 0 recovers the clock from the signal.
//	events := make(chan decoder.Event, 1024)
//	dec, err := decoder.New(events, 0)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer dec.Close()
//
//	for bit := range dec.Bits() {
//	    _ = bit // decoder.High, decoder.Low or decoder.Invalid
//	}
//
// The README of the module covers framing, timing, clock recovery and the
// GPIO wiring the demos use.
package manchester
