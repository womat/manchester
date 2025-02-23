package encoder

import (
	"log/slog"
	"strconv"
	"sync"
	"time"
)

const (
	Low  = 0 // Low represents a low signal level.
	High = 1 // High represents a high signal level.
)

// Encoder implements a Manchester encoder that runs in the background.
type Encoder struct {
	writeMutex  sync.Mutex   // Mutex to synchronize Write() access
	carrierFreq float64      // Carrier frequency in Hz (e.g., 50 Hz)
	buffer      chan frame   // Buffered channel for outgoing data
	bitTicker   *time.Ticker // Ticker for timing bit transitions
	setValue    SetValue     // Function to set the GPIO output level

	stop chan struct{}  // stop is the channel to stop the Encoder.
	wg   sync.WaitGroup // wg signals that Decoder is Encoder and closed.
}

// SetValue is a function type that sets the GPIO output level.
type SetValue func(int) error

type frame struct {
	b       byte // b is the byte to be encoded
	framing bool // framing is true if the b should be framed with start and stop bits
}

// New creates a new Manchester encoder with the specified carrier frequency.
func New(carrierFreq float64, setValue SetValue) *Encoder {
	bitPeriod := time.Second / time.Duration(carrierFreq)
	e := &Encoder{
		carrierFreq: carrierFreq,
		buffer:      make(chan frame, 1024),
		stop:        make(chan struct{}),
		bitTicker:   time.NewTicker(bitPeriod / 2), // Halbbit-Dauer
		setValue:    setValue,
	}

	e.wg.Add(1)
	go e.listenForFrames()
	return e
}

// listenForFrames launches the encoder in a background goroutine.
func (e *Encoder) listenForFrames() {
	defer e.wg.Done()

	for {
		select {
		case f := <-e.buffer:
			e.encodeFrame(f)
		case <-e.stop:
			// Ensure all remaining bytes are processed before stopping
			close(e.buffer)

			for f := range e.buffer {
				e.encodeFrame(f)
			}

			e.bitTicker.Stop()
			return
		}
	}
}

// Close gracefully shuts down the encoder, ensuring all writes complete.
func (e *Encoder) Close() error {
	close(e.stop)
	e.wg.Wait()
	return nil
}

// encodeByte converts a byte into Manchester-coded bits and transmits it.
func (e *Encoder) encodeFrame(f frame) {

	if f.framing {
		slog.Debug("Send Byte with framing", "char", string(f.b))
		slog.Debug("Send Start Bit", "bit", 0)
		// Send start bit (0)
		e.encodeBit(Low)
	} else {
		slog.Debug("Send Byte without framing", "byte", "0x"+strconv.FormatInt(int64(f.b), 16))
	}

	// Send data bits (LSB first)
	for i := 0; i < 8; i++ { // Von Bit 0 bis Bit 7
		bit := (f.b >> i) & 1
		slog.Debug("Send Bit", "bit", bit)
		e.encodeBit(bit)
	}

	if f.framing {
		slog.Debug("Send Stop Bit", "bit", 1)
		// Send stop bit (1)
		e.encodeBit(High)
	}
}

// encodeBit sends a single Manchester-encoded bit.
func (e *Encoder) encodeBit(bit byte) {
	if bit == Low {
		_ = e.setValue(0)
		<-e.bitTicker.C // Wait for the first half-bit
		_ = e.setValue(1)
		<-e.bitTicker.C // Wait for the second half-bit
	} else {
		_ = e.setValue(1)
		<-e.bitTicker.C // Wait for the first half-bit
		_ = e.setValue(0)
		<-e.bitTicker.C // Wait for the second half-bit
	}
}

// Write places data into the buffer and ensures proper synchronization.
func (e *Encoder) Write(data []byte) (int, error) {
	e.writeMutex.Lock() // Ensure exclusive write access
	defer e.writeMutex.Unlock()

	// Send 16 high bits for synchronization
	e.buffer <- frame{b: 0xff, framing: false}
	e.buffer <- frame{b: 0xff, framing: false}
	for _, b := range data {
		e.buffer <- frame{b: b, framing: true}
	}

	return len(data), nil
}
