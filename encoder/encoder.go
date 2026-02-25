// Package encoder implements a Manchester encoder for GPIO pins.
// It supports configurable bit order (LSB/MSB), optional sync bytes,
// non-blocking writes, and can be used with any GPIO implementation
// that provides a SetValue(Level) error function.
//
// Example usage:
//
//
//    func main() {
//        // Initialize GPIO pin 17
//        pin, err := rpi.NewPin(17)
//        if err != nil {
//            log.Fatal(err)
//        }
//        defer pin.Close()
//
//        // Configure as output
//        if err := pin.SetMode(gpioemu.Output); err != nil {
//            log.Fatal(err)
//        }
//
//        // Create a SetValue function for the encoder
//        setValue := func(level encoder.Level) error {
//            return pin.SetValue(level)
//        }
//
//        // Create encoder with 50Hz carrier, LSB first, 2 sync bytes
//        enc := encoder.New(
//            50,
//            setValue,
//            encoder.WithBitOrder(encoder.LSBFirst),
//            encoder.WithSyncBytes(2),
//        )
//        defer enc.Close()
//
//        // Send "Hallo World"
//        _, err = enc.Write([]byte("Hallo World"))
//        if err != nil {
//            log.Fatal(err)
//        }
// 	  // Wait for transmission to complete
// 	  enc.Flush()
//    }

package encoder

import (
	"errors"
	"log/slog"
	"sync"
	"time"
)

const (
	Low  = 0 // Low represents a low signal level.
	High = 1 // High represents a high signal level.
)

type BitOrder int
type Option func(*Encoder)

const (
	LSBFirst BitOrder = iota
	MSBFirst
)

var ErrEncoderStopped = errors.New("encoder stopped")

// Encoder implements a Manchester encoder that runs in the background.
type Encoder struct {
	writeMutex  sync.Mutex   // Mutex to synchronize Write() access
	carrierFreq uint         // Carrier frequency in Hz (e.g., 50 Hz)
	bitOrder    BitOrder     // Order of bits: LSBFirst or MSBFirst
	syncBytes   int          // Number of 0xFF bytes for synchronization before actual data
	buffer      chan frame   // Buffered channel for outgoing frames
	bitTicker   *time.Ticker // Ticker for timing bit transitions
	setValue    SetValue     // Function to set the GPIO output level
	bufferSize  int

	stop chan struct{}  // Channel to stop the Encoder
	wg   sync.WaitGroup // WaitGroup to track the encoder goroutine
}

// SetValue is a function type that sets the GPIO output level.
type SetValue func(int) error

type frame struct {
	b       byte // Byte to be encoded
	framing bool // If true, add start/stop bits
}

// New creates a new Manchester encoder with the specified carrier frequency.
func New(carrierFreq uint, setValue SetValue, opts ...Option) *Encoder {
	if carrierFreq <= 0 {
		panic("carrierFreq must be > 0")
	}
	e := &Encoder{
		carrierFreq: carrierFreq,
		bitOrder:    LSBFirst,
		syncBytes:   2,
		bufferSize:  1024,
		setValue:    setValue,
		stop:        make(chan struct{}),
	}

	for _, opt := range opts {
		opt(e)
	}

	bitPeriod := time.Second / time.Duration(carrierFreq)
	e.buffer = make(chan frame, e.bufferSize)
	e.bitTicker = time.NewTicker(bitPeriod / 2)

	e.wg.Add(1)
	go e.listenForFrames()
	return e
}

// WithBitOrder sets the bit order (LSB/MSB) for the encoder.
func WithBitOrder(order BitOrder) Option {
	return func(e *Encoder) {
		e.bitOrder = order
	}
}

// WithSyncBytes sets the number of 0xFF sync bytes sent before actual data.
func WithSyncBytes(n int) Option {
	return func(e *Encoder) {
		if n >= 0 {
			e.syncBytes = n
		}
	}
}

// WithBufferSize sets the size of the internal buffer channel.
func WithBufferSize(size int) Option {
	return func(e *Encoder) {
		if size > 0 {
			e.bufferSize = size
		}
	}
}

// WithoutSync disables the initial sync bytes.
func WithoutSync() Option {
	return func(e *Encoder) {
		e.syncBytes = 0
	}
}

// Close gracefully shuts down the encoder.
func (e *Encoder) Close() error {
	close(e.stop)
	e.wg.Wait()

	if e.bitTicker != nil {
		e.bitTicker.Stop()
	}
	return nil
}

// Flush waits until all buffered frames have been transmitted.
func (e *Encoder) Flush() {
	halfBitDuration := time.Second / time.Duration(e.carrierFreq*2)

	for {
		e.writeMutex.Lock()
		bufferLen := len(e.buffer)
		e.writeMutex.Unlock()

		if bufferLen == 0 {
			// Buffer is empty → wait a little to ensure the last bit is fully transmitted
			time.Sleep(2 * halfBitDuration) // Stop bit + last half-bit
			return
		}

		// Buffer is not empty → wait for all bytes in the buffer
		waitTime := time.Duration(20*bufferLen) * halfBitDuration // 10 bits per byte * 2 half-bits
		time.Sleep(waitTime)
	}
}

// Write places data into the buffer for transmission.
func (e *Encoder) Write(data []byte) (int, error) {
	e.writeMutex.Lock()
	defer e.writeMutex.Unlock()

	// Send sync bytes
	for i := 0; i < e.syncBytes; i++ {
		select {
		case e.buffer <- frame{b: 0xff, framing: false}:
		case <-e.stop:
			return 0, ErrEncoderStopped
		}
	}

	// Send data bytes
	for _, b := range data {
		select {
		case e.buffer <- frame{b: b, framing: true}:
		case <-e.stop:
			return 0, ErrEncoderStopped
		}
	}

	return len(data), nil
}

// encodeByte converts a byte into Manchester-coded bits and transmits it.
func (e *Encoder) encodeFrame(f frame) {

	if f.framing {
		e.encodeBit(Low) // Start bit (0)
	}

	switch e.bitOrder {
	case LSBFirst: //  Bit 0 to Bit 7
		for i := 0; i < 8; i++ {
			e.encodeBit((f.b >> i) & 1)
		}
	case MSBFirst: //  Bit 7 to Bit 0
		for i := 7; i >= 0; i-- {
			e.encodeBit((f.b >> i) & 1)
		}
	}

	if f.framing {
		e.encodeBit(High) // Stop bit (1)
	}
}

// Manchester encoding lookup table: [bit][half-step]
var manchester = [2][2]int{
	Low:  {Low, High},
	High: {High, Low},
}

// encodeBit sends a single Manchester-encoded bit.
func (e *Encoder) encodeBit(bit byte) {
	// Define the two half-bit levels based on Manchester coding
	for _, v := range manchester[bit] {
		e.setBit(v)

		select {
		case <-e.bitTicker.C:
		case <-e.stop:
			return
		}
	}
}

// setBit sets the GPIO level and logs any error.
func (e *Encoder) setBit(v int) {
	if err := e.setValue(v); err != nil {
		slog.Error("setValue error:", err)
	}
}

// listenForFrames launches the encoder in a background goroutine.
func (e *Encoder) listenForFrames() {
	defer e.wg.Done()

	for {
		select {
		case f := <-e.buffer:
			e.encodeFrame(f)
		case <-e.stop:
			// Drain remaining frames
			for {
				select {
				case f := <-e.buffer:
					e.encodeFrame(f)
				default:
					return
				}
			}
		}
	}
}
