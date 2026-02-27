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
//        // Create encoder with 50Hz bit clock, LSB first, 2 sync bytes
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
// 	  enc.Wait()
//    }

package encoder

import (
	"errors"
	"log/slog"
	"sync"
	"time"
)

// SetValue is a function type that sets the GPIO output level.
type SetValue func(Level) error

// txByte represents a byte to transmit along with a flag indicating whether to add start/stop bits.
type txByte struct {
	b            byte // Byte to transmit
	addStartStop bool // Add start/stop bits
}

type Level int
type BitOrder int
type Option func(*Encoder)

// ManchesterEncoding represents the type of Manchester encoding to use.
type ManchesterEncoding int

const (
	Low  Level = 0 // Low represents a low signal level.
	High Level = 1 // High represents a high signal level.
)

const (
	LSBFirst BitOrder = iota
	MSBFirst
)

const (
	IEEE ManchesterEncoding = iota
	Thomas
)

var ErrEncoderStopped = errors.New("encoder stopped")

// Encoder implements a Manchester encoder that runs in the background.
type Encoder struct {
	writeMutex         sync.Mutex         // Mutex to synchronize Write() access
	bitClockHz         int                // frequency in Hz (e.g., 50 Hz)
	bitOrder           BitOrder           // Order of bits: LSBFirst or MSBFirst
	syncBytes          int                // Number of 0xFF bytes for synchronization before actual data
	buffer             chan txByte        // Buffered channel for outgoing txBytes
	halfBitTicker      *time.Ticker       // Ticker for Manchester half-bit transitions
	setValue           SetValue           // Function to set the GPIO output level
	bufferSize         int                // Size of the internal buffer channel
	manchesterEncoding ManchesterEncoding // Type of Manchester encoding (e.g., IEEE vs. Thomas)
	encodingTable      [2][2]Level        // Manchester encoding lookup table: [bit][half-step]

	stop    chan struct{}  // Channel to stop the Encoder
	wg      sync.WaitGroup // WaitGroup to track the encoder goroutine
	wgBytes sync.WaitGroup // tracks bytes that are fully transmitted

}

// New creates a new Manchester encoder with the specified bit clock frequency.
func New(bitClockHz int, setValue SetValue, opts ...Option) *Encoder {
	if bitClockHz <= 0 {
		panic("bitClockHz must be > 0")
	}
	e := &Encoder{
		bitClockHz:         bitClockHz,
		bitOrder:           LSBFirst,
		syncBytes:          2,
		bufferSize:         1024,
		setValue:           setValue,
		stop:               make(chan struct{}),
		manchesterEncoding: IEEE,
	}

	for _, opt := range opts {
		opt(e)
	}

	bitPeriod := time.Second / time.Duration(bitClockHz)
	e.halfBitTicker = time.NewTicker(bitPeriod / 2)
	e.encodingTable = encodingTable(e.manchesterEncoding)
	e.buffer = make(chan txByte, e.bufferSize)

	e.wg.Add(1)
	go e.processTxBytes()
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

// WithManchesterEncoding sets the type of Manchester encoding (e.g., IEEE or Thomas).
func WithManchesterEncoding(enc ManchesterEncoding) Option {
	return func(e *Encoder) {
		e.manchesterEncoding = enc
		e.encodingTable = encodingTable(enc)
	}
}

// Close gracefully shuts down the encoder.
func (e *Encoder) Close() error {
	// Signal stop to prevent new writes
	close(e.stop)

	// Close buffer to let listener finish draining
	close(e.buffer)

	// Wait for background goroutine to finish
	e.wg.Wait()

	// Stop ticker after goroutine finished
	if e.halfBitTicker != nil {
		e.halfBitTicker.Stop()
	}

	return nil
}

// Wait waits until all buffered txBytes have been transmitted.
func (e *Encoder) Wait() {
	e.wgBytes.Wait() // block until all bytes fully transmitted
}

// Write places data into the buffer for transmission.
func (e *Encoder) Write(data []byte) (int, error) {
	e.writeMutex.Lock()
	defer e.writeMutex.Unlock()

	// Send sync bytes
	for i := 0; i < e.syncBytes; i++ {
		select {
		case e.buffer <- txByte{b: 0xff, addStartStop: false}:
			e.wgBytes.Add(1)
		case <-e.stop:
			return 0, ErrEncoderStopped
		}
	}

	// Send data bytes
	for _, b := range data {
		select {
		case e.buffer <- txByte{b: b, addStartStop: true}:
			e.wgBytes.Add(1)
		case <-e.stop:
			return 0, ErrEncoderStopped
		}
	}

	return len(data), nil
}

// encodeByte encodes a single byte and transmits it with optional start/stop bits.
func (e *Encoder) encodeByte(b byte, addStartStop bool) {

	defer e.wgBytes.Done() // mark this byte as fully transmitted

	if addStartStop {
		e.encodeBit(byte(Low)) // Start bit (0)
	}

	switch e.bitOrder {
	case LSBFirst: //  Bit 0 to Bit 7
		for i := 0; i < 8; i++ {
			e.encodeBit((b >> i) & 1)
		}
	case MSBFirst: //  Bit 7 to Bit 0
		for i := 7; i >= 0; i-- {
			e.encodeBit((b >> i) & 1)
		}
	}

	if addStartStop {
		e.encodeBit(byte(High)) // Stop bit (1)
	}
}

// encodeBit sends a single Manchester-encoded bit.
func (e *Encoder) encodeBit(bit byte) {
	// Define the two half-bit levels based on Manchester coding
	for _, v := range e.encodingTable[bit] {
		e.setBit(v)

		select {
		case <-e.halfBitTicker.C:
		case <-e.stop:
			return
		}
	}
}

// setBit sets the GPIO level and logs any error.
func (e *Encoder) setBit(v Level) {
	if err := e.setValue(v); err != nil {
		slog.Error("setValue error:", err)
	}
}

// processTxBytes runs in the background and transmits bytes from the buffer.
func (e *Encoder) processTxBytes() {
	defer e.wg.Done()

	// Iterate over channel until it's closed and drained
	for tx := range e.buffer {
		e.encodeByte(tx.b, tx.addStartStop)
	}
}

// encodingTable returns the Manchester encoding lookup table for the given encoding type.
func encodingTable(code ManchesterEncoding) [2][2]Level {
	switch code {
	case IEEE:
		return [2][2]Level{
			High: {Low, High},
			Low:  {High, Low},
		}
	case Thomas:
		return [2][2]Level{
			High: {High, Low},
			Low:  {Low, High},
		}
	default:
		panic("unsupported Manchester encoding")
	}
}
