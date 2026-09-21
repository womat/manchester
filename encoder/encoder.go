// Package encoder implements a Manchester encoder for GPIO pins.
// It supports configurable bit order (LSB/MSB), optional sync bytes,
// non-blocking writes, and can be used with any GPIO implementation
// that provides a SetValue(Level) error function.
//
// # Example usage
//
//	func main() {
//	    pin, err := rpi.NewPin(17, rpi.WithMode(gpio.Output))
//	    if err != nil {
//	        log.Fatal(err)
//	    }
//	    defer pin.Close()
//
//	    enc, err := encoder.New(
//	        50,
//	        func(level encoder.Level) error {
//	            return pin.SetValue(gpio.Level(level))
//	        },
//	        encoder.WithBitOrder(encoder.LSBFirst),
//	        encoder.WithSyncBytes(2),
//	    )
//	    if err != nil {
//	        log.Fatal(err)
//	    }
//	    defer enc.Close()
//
//	    _, err = enc.Send([]byte("Hello World"))
//	    if err != nil {
//	        log.Fatal(err)
//	    }
//	    enc.Wait()
//	}
package encoder

import (
	"context"
	"errors"
	"fmt"
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
	halfBitPeriod      time.Duration      // Duration of one Manchester half-bit
	setValue           SetValue           // Function to set the GPIO output level
	bufferSize         int                // Size of the internal buffer channel
	manchesterEncoding ManchesterEncoding // Type of Manchester encoding (e.g., IEEE vs. Thomas)
	encodingTable      [2][2]Level        // Manchester encoding lookup table: [bit][half-step]
	onError            func(err error)    // Optional error handler callback

	closed    bool // guarded by writeMutex: true once Close() shut the buffer down
	cancel    context.CancelFunc
	ctx       context.Context
	wg        sync.WaitGroup // WaitGroup to track the encoder goroutine
	closeOnce sync.Once      // Ensures Close() is only executed once
	wgBytes   sync.WaitGroup // tracks bytes that are fully transmitted

}

// New creates a new Manchester encoder with the specified bit clock frequency
// and starts the transmitting goroutine.
//
// It returns an error if bitClockHz is not positive, if setValue is nil, or if
// an unsupported Manchester encoding was selected.
// Call Close() to stop the encoder and wait for a clean shutdown.
func New(bitClockHz int, setValue SetValue, opts ...Option) (*Encoder, error) {
	if bitClockHz <= 0 {
		return nil, fmt.Errorf("bit clock must be greater than 0, got %d", bitClockHz)
	}

	if setValue == nil {
		return nil, errors.New("setValue must not be nil")
	}

	e := &Encoder{
		bitClockHz:         bitClockHz,
		bitOrder:           LSBFirst,
		syncBytes:          2,
		bufferSize:         1024,
		setValue:           setValue,
		manchesterEncoding: IEEE,
	}

	for _, opt := range opts {
		opt(e)
	}

	// validate manchesterEncoding after options are applied
	switch e.manchesterEncoding {
	case IEEE, Thomas:
		e.encodingTable = encodingTable(e.manchesterEncoding)
	default:
		return nil, fmt.Errorf("unsupported Manchester encoding: %v", e.manchesterEncoding)
	}

	bitPeriod := time.Second / time.Duration(bitClockHz)
	e.halfBitPeriod = bitPeriod / 2
	e.buffer = make(chan txByte, e.bufferSize)

	e.ctx, e.cancel = context.WithCancel(context.Background())
	e.wg.Add(1)
	go e.processTxBytes()
	return e, nil
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

// WithErrorHandler sets a callback that is called when a GPIO error occurs during transmission.
// If not set, GPIO errors are silently ignored.
func WithErrorHandler(fn func(err error)) Option {
	return func(e *Encoder) {
		e.onError = fn
	}
}

// WithManchesterEncoding sets the type of Manchester encoding (e.g., IEEE or Thomas).
func WithManchesterEncoding(enc ManchesterEncoding) Option {
	return func(e *Encoder) {
		e.manchesterEncoding = enc
	}
}

// Close gracefully shuts down the encoder.
// It is safe to call Close multiple times.
//
// A transmission in progress is aborted, which leaves the line at the level of
// the half-bit that was driven last. Callers that need a defined idle level
// must set it themselves after Close returns.
func (e *Encoder) Close() error {
	e.closeOnce.Do(func() {

		// Signal stop to prevent new writes
		e.cancel()

		// Lock to prevent new writes while closing. Send() marks the buffer as
		// closed under the same mutex, so no Send can reach the channel after it
		// has been closed.
		e.writeMutex.Lock()
		e.closed = true
		// Close buffer to let listener finish draining
		close(e.buffer)
		e.writeMutex.Unlock()

		// Wait for background goroutine to finish
		e.wg.Wait()
	})
	return nil
}

// Wait waits until all buffered txBytes have been transmitted.
//
// It must not be called concurrently with Send: a Send that raises the counter
// from zero while Wait is already blocked is a data race on the WaitGroup.
func (e *Encoder) Wait() {
	e.wgBytes.Wait() // block until all bytes fully transmitted
}

// Send places data into the transmission buffer.
//
// It blocks if the buffer is full and returns ErrEncoderStopped if Close() was
// called, either before or while waiting for buffer space. Bytes that were
// already queued when the encoder was stopped are discarded untransmitted.
//
// Send must not be called concurrently with Wait.
func (e *Encoder) Send(data []byte) (int, error) {
	e.writeMutex.Lock()
	defer e.writeMutex.Unlock()

	// The buffer is closed under writeMutex, so checking the flag here is
	// enough to guarantee that the sends below never touch a closed channel.
	if e.closed {
		return 0, ErrEncoderStopped
	}

	// Send sync bytes
	for i := 0; i < e.syncBytes; i++ {
		if !e.queue(txByte{b: 0xff, addStartStop: false}) {
			return 0, ErrEncoderStopped
		}
	}

	// Send data bytes
	for _, b := range data {
		if !e.queue(txByte{b: b, addStartStop: true}) {
			return 0, ErrEncoderStopped
		}
	}

	return len(data), nil
}

// queue hands a single byte to the transmitting goroutine, blocking while the
// buffer is full. It reports false if the encoder was stopped meanwhile.
//
// The caller must hold writeMutex.
//
// wgBytes is incremented before the byte becomes visible to the consumer:
// the other way round the consumer could call Done() before Add(), which
// drives the counter negative.
func (e *Encoder) queue(tx txByte) bool {
	e.wgBytes.Add(1)

	select {
	case e.buffer <- tx:
		return true
	case <-e.ctx.Done():
		e.wgBytes.Done() // the byte never made it into the buffer
		return false
	}
}

// encodeByte encodes a single byte and transmits it with optional start/stop bits.
func (e *Encoder) encodeByte(b byte, addStartStop bool) {

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
	select {
	case <-e.ctx.Done():
		return
	default:
	}

	for _, v := range e.encodingTable[bit] {
		e.setBit(v)

		if !e.waitHalfBit() {
			return
		}
	}
}

// waitHalfBit blocks for one half-bit period, starting from the moment the
// level was driven.
//
// The period is deliberately measured from now instead of from a running
// schedule: the decoder validates every interval on its own against the
// nominal bit time, so a half-bit that runs slightly long is harmless, while
// a shortened one is decoded as an invalid bit. Catching up on a late half-bit
// by shortening the next one would therefore corrupt the signal rather than
// repair it, and a free-running ticker does exactly that - after an idle
// period its buffered tick truncates the first half-bits of the next message.
//
// It reports false if the encoder was stopped while waiting.
func (e *Encoder) waitHalfBit() bool {
	timer := time.NewTimer(e.halfBitPeriod)
	defer timer.Stop()

	select {
	case <-e.ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// setBit sets the GPIO level and logs any error.
func (e *Encoder) setBit(v Level) {
	if err := e.setValue(v); err != nil {
		if e.onError != nil {
			e.onError(fmt.Errorf("failed to set GPIO level: %w", err))
		}
	}
}

// processTxBytes runs in the background and transmits bytes from the buffer.
func (e *Encoder) processTxBytes() {

	defer e.wg.Done()

	for {
		select {
		case <-e.ctx.Done():
			// drain remaining bytes without transmitting them
			for range e.buffer {
				e.wgBytes.Done()
			}
			return
		case tx, open := <-e.buffer:
			if !open {
				return
			}

			e.encodeByte(tx.b, tx.addStartStop)
			e.wgBytes.Done() // mark this byte as fully transmitted
		}
	}
}

// encodingTable returns the Manchester encoding lookup table for the given
// encoding type, mapping a bit to the levels of its two half-bits.
//
// Unsupported encodings are rejected by New(), so this falls back to IEEE.
func encodingTable(code ManchesterEncoding) [2][2]Level {
	switch code {
	case Thomas:
		return [2][2]Level{
			High: {High, Low},
			Low:  {Low, High},
		}
		// default IEEE
	default:
		return [2][2]Level{
			High: {Low, High},
			Low:  {High, Low},
		}
	}
}
