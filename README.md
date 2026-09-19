# manchester

Manchester line coding: `encoder` turns bytes into a sequence of line levels,
`decoder` turns a stream of edge events back into bits.

Manchester encodes every bit as two half-bits of opposite level, so each bit
carries a transition in its middle. That transition is the data, and it is also
what lets a receiver recover the clock from the signal itself. Two conventions
exist and both are supported:

| Convention        | logical 1          | logical 0          |
|-------------------|--------------------|--------------------|
| IEEE 802.3 (default) | low → high at mid-bit | high → low at mid-bit |
| G.E. Thomas       | high → low at mid-bit | low → high at mid-bit |

Both sides must agree. Decoding IEEE data with the Thomas table yields the
bitwise complement of the payload.

## How the pieces fit

Neither package imports `gpio`, and they do not import each other:

- the **encoder** writes through a `func(Level) error` callback,
- the **decoder** reads `decoder.Event` values from a channel.

The application provides the glue. That keeps the codec usable over any
transport — a GPIO pin, a radio module, a file — and it is why the integration
tests can wire both ends together in memory. See `demo/manchester_sender` and
`demo/manchester_listener` for the GPIO wiring, and `roundtrip_test.go` in this
directory for the in-memory variant.

```
bytes ──▶ encoder ──▶ SetValue(level) ──▶ line ──▶ edge events ──▶ decoder ──▶ bits
```

## Sending

```go
enc, err := encoder.New(50,
    func(level encoder.Level) error { return pin.SetValue(gpio.Level(level)) },
    encoder.WithBitOrder(encoder.LSBFirst),
    encoder.WithSyncBytes(2),
)
if err != nil {
    log.Fatal(err)
}
defer enc.Close()

if _, err := enc.Send([]byte("Hello World")); err != nil {
    log.Fatal(err)
}
enc.Wait() // block until everything has been transmitted
```

`Send` frames every data byte with a low start bit and a high stop bit, and
prefixes the message with `WithSyncBytes(n)` sync bytes (`0xff`, sent without
framing). Defaults: 50 … whatever you pass as bit clock, `LSBFirst`, two sync
bytes, a buffer of 1024 bytes, IEEE.

The encoder transmits from a background goroutine. `Send` blocks while the
buffer is full and returns `ErrEncoderStopped` once `Close` was called; `Wait`
blocks until the buffer has drained. `Wait` must not be called concurrently
with `Send`.

## Receiving

```go
dec, err := decoder.New(events, 50, decoder.WithManchesterEncoding(decoder.IEEE))
if err != nil {
    log.Fatal(err)
}
defer dec.Close()

for bit := range dec.Bits() {
    // bit is decoder.High, decoder.Low or decoder.Invalid
}
```

The bit clock argument decides how the timing is established:

- **greater than zero** — the bit periods are computed from it directly.
  This is the normal case; the demos pass `-bitClock 50`.
- **zero** — clock discovery: the decoder measures incoming edge intervals and
  derives the bit period from their median. It needs 500 intervals before the
  first bit appears, roughly 7 seconds of continuous signal at 50 Hz, and
  everything received until then is discarded.
- **negative** — rejected, as is a nil event channel.

An interval that matches neither a half nor a full bit period (±25 %) is
reported as `Invalid`. After more than 20 consecutive invalid intervals the
decoder discards its timing and returns to clock discovery. `Info()` reports the
current state, the recovered frequency, and the counters for dropped bits and
resynchronisations; it is safe to call from another goroutine.

## What the decoder does not do

It delivers a raw bit stream. Start and stop bits, sync preambles and byte
boundaries are the caller's business — `demo/manchester_listener` shows the
reassembly. Two properties matter when reading that stream:

1. **The first bit of a transmission is never reported.** A bit is decoded from
   the interval between two edges, so the first edge only establishes the
   reference timestamp.
2. **The bit phase only locks at the first full-bit interval.** A run of
   identical bits produces nothing but half-bit intervals, and from those the
   position of the mid-bit edge cannot be derived.

The sync preamble covers both: it absorbs the lost first bit, and the low start
bit of the first data byte provides the full-bit interval that locks the phase.
Senders that use `WithoutSync()` must expect to lose the beginning of every
message.

Clock discovery has a related limitation: an alternating bit pattern (`0x55`,
`0xaa`, …) has a transition in every bit and none on the bit boundaries, so
every interval is a full-bit one. With no short intervals to compare against the
discovery locks on to twice the real period. A preamble of `0xff` bytes avoids
this, which is another reason to keep it.

## Timing

The encoder waits one half-bit period after driving each level, measured from
that moment and **without compensating** for a late wakeup. This is deliberate:
the decoder validates every interval on its own against the nominal bit time, so
a half-bit that runs slightly long is harmless, while a shortened one is decoded
as invalid. Catching up on a late half-bit by shortening the next one corrupts
the signal instead of repairing it.

`Close()` aborts a transmission in progress and leaves the line at the level of
the half-bit driven last. There is no idle-level option; callers that need a
defined idle state must set it themselves afterwards.

## Tests

`roundtrip_test.go` in this directory wires encoder and decoder into a virtual
line and covers both conventions, both bit orders, the polarity of the encoding
and the encoder's half-bit timing. It lives here rather than in either package
because it must import both. The packages themselves carry unit tests for their
internals — framing and bit order in `encoder`, clock discovery and event
handling in `decoder`.

```sh
go test ./manchester/...
go test -race ./manchester/...
go test ./manchester/decoder/ -run TestCalcBitPeriods -v
```

Full API: `go doc github.com/womat/golib/manchester/encoder` and
`.../manchester/decoder`.
