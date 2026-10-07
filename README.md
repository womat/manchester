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
framing).

| Encoder option | Default | Comment |
|---|---|---|
| `WithBitOrder(LSBFirst\|MSBFirst)` | `LSBFirst` | order the bits of each byte go on the line |
| `WithSyncBytes(n)` | `2` | `0xff` bytes sent unframed ahead of the data; negative values ignored |
| `WithoutSync()` | — | shorthand for `WithSyncBytes(0)`; read [the caveat](#what-the-decoder-does-not-do) first |
| `WithBufferSize(n)` | `1024` | bytes the internal channel holds before `Send` blocks; values ≤ 0 ignored |
| `WithManchesterEncoding(IEEE\|Thomas)` | `IEEE` | must match the decoder |
| `WithErrorHandler(func(error))` | none | **without it, a failing `SetValue` is silently ignored** — the transmission continues with a corrupt line |

`WithErrorHandler` is the one option worth setting in production: the encoder
transmits from a background goroutine, so an error from the `SetValue` callback
has nowhere to be returned to. Unhandled, it disappears.

The encoder transmits from a background goroutine. `Send` blocks while the
buffer is full and returns `ErrEncoderStopped` once `Close` was called; `Wait`
blocks until the buffer has drained. `Wait` must not be called concurrently
with `Send`.

## Receiving

```go
dec, err := decoder.New(events, 0, decoder.WithManchesterEncoding(decoder.IEEE)) // 0: any bit rate
if err != nil {
    log.Fatal(err)
}
defer dec.Close()

for bit := range dec.Bits() {
    // bit is decoder.High, decoder.Low or decoder.Invalid
}
```

| Decoder option | Default | Comment |
|---|---|---|
| `WithManchesterEncoding(IEEE\|Thomas)` | `IEEE` | must match the encoder |
| `WithBufferSize(n)` | `1024` | bits the `Bits()` channel holds before bits are dropped and counted |
| `WithLogger(*slog.Logger)` | discard | per-interval debug output; expensive at speed, meant for bringing a link up |

The bit clock argument decides how the timing is established:

- **zero** — clock recovery, the decoder reads any bit rate. This is what a
  receiver of a third-party sender wants, and what the demos default to.
- **greater than zero** — the bit periods are computed from it directly, and
  must match the sender within the tolerance.
- **negative** — rejected, as is a nil event channel.

Clock recovery works on the edges alone, in three steps:

1. **Lock.** The decoder buffers edges until the latest 16 intervals all fit
   one half-bit period T or twice that, with both lengths present. Manchester
   knows no other interval, and a window with only one length is ambiguous: a
   run of identical bits gives only T, alternating bits (`0x55`, `0xaa`, …)
   only 2T, which looks like T of a clock half as fast. The decoder keeps
   waiting there instead of guessing.
2. **Phase.** A full-bit interval always runs from one mid-bit edge to the
   next, and so does every second edge before it. From the first full-bit
   interval the decoder decodes the buffered edges backwards, so a preamble of
   identical bits — the encoder's sync bytes, a DL-Bus SYNC — arrives in full,
   first bit included. Bits are delayed only until then; afterwards they
   arrive edge by edge.
3. **Tracking.** Every valid interval moves the clock by a sixteenth of its
   deviation, so the decoder follows a sender whose clock is off the nominal
   rate or drifts. The tests cover ±5 % offset, 10 % drift over a frame and
   ±5 % jitter on every half-bit.

An interval that matches neither a half nor a full bit period (±25 %) is
reported as `Invalid`. So is the interval to an event with `Missed > 0`, the
number of edges lost immediately before it — whatever its length, it spans
more than one edge, and it never enters the clock estimate. The glue sets
`Missed` from `gpio.Event.Missed` and adds the edges it had to drop itself, see
`demo/manchester_listener`. With a recovered clock an invalid interval — a
pause between two transmissions, too — also resets the phase, which is settled
again at the next full-bit interval; the clock is kept. After more than 20
consecutive invalid intervals the decoder discards its timing and recovers
the clock anew, also when it was configured. `Info()` reports the current
state, the current frequency, the buffer overflow count (bits dropped because
the consumer did not keep up) and the resync count; it is safe to call from
another goroutine.

## What the decoder does not do

It delivers a raw bit stream. Start and stop bits, sync preambles, byte
boundaries and the polarity of the line are the caller's business —
`demo/manchester_listener` shows the reassembly. Line drivers such as an
optocoupler often invert the signal, which turns IEEE into Thomas and the
other way round; a caller that knows its preamble can detect that from the
decoded bits.

With a **configured** bit clock the decoder does not buffer, and two
properties matter when reading the stream:

1. **The first bit of a transmission is never reported.** A bit is decoded from
   the interval between two edges, so the first edge only establishes the
   reference timestamp.
2. **The bit phase is guessed until the first full-bit interval.** A run of
   identical bits produces nothing but half-bit intervals, and from those the
   position of the mid-bit edge cannot be derived; a wrong guess is corrected
   there.

The sync preamble covers both: it absorbs the lost first bit, and the low start
bit of the first data byte provides the full-bit interval that settles the
phase. Senders that use `WithoutSync()` must expect to lose the beginning of
every message. With a **recovered** clock neither applies — the buffered edges
are decoded backwards once the phase is known — but the first bits arrive only
after the lock, see above.

## Timing

Half-bits end on a fixed schedule, so the small delay of every wakeup does not
add up over a message. When the encoder falls behind that schedule by more than
an eighth of a half-bit — after an idle period, or after the system stalled it —
it restarts the schedule instead of **catching up**. This is deliberate: the
decoder validates every interval on its own against the nominal bit time, so a
half-bit that runs slightly long is harmless, while a shortened one is decoded
as invalid. Catching up on a late half-bit by shortening the next one corrupts
the signal instead of repairing it.

Go's timers alone are too coarse for this. On Linux the runtime waits for them
in `epoll_wait`, which counts whole milliseconds: a 500 µs timer fires after
about 1 ms, a 2.5 ms one after 3 ms. The encoder therefore sleeps the last 2 ms
of every half-bit in `nanosleep`, on its own OS thread with the timer slack
reduced from the default 50 µs to 1 ns. Off Linux it falls back to `time.Sleep`.

Measured on a Raspberry Pi 400 (encoder on one GPIO, decoder on another, looped
back through a PC817 optocoupler, 50 messages of 56 bytes per rate):

| Bit rate | half-bit error p99 | messages without error | with `chrt -f 50` and `GOGC=off` |
|---|---|---|---|
| 1000 bit/s | 3.3 % | 44/50 | 49/50 |
| 2000 bit/s | 3.7 % | 48/50 | 45/50 |
| 3000 bit/s | 12–15 % | not measured | 33/50 |

Before this change, half-bits ran 115 % long at 1000 bit/s and nothing was
decoded above 200 bit/s. What remains are rare stalls of up to about 1.5 ms,
roughly 1 in 7000 half-bits at 1000 bit/s, caused by the garbage collector and
by the scheduler together; each one costs the message it falls into. A sender
that needs every message must add a checksum and retransmit — see
[Not addressed](#not-addressed). Above about 3000 bit/s the fixed cost of
driving the line and waking up approaches the tolerance on every half-bit.

`Close()` aborts a transmission in progress and leaves the line at the level of
the half-bit driven last. There is no idle-level option; callers that need a
defined idle state must set it themselves afterwards.

## Tests

`roundtrip_test.go` in this directory wires encoder and decoder into a virtual
line and covers both conventions, both bit orders, a configured and a recovered
clock, the polarity of the encoding and the encoder's half-bit timing. It lives
here rather than in either package because it must import both. The packages
themselves carry unit tests for their internals — framing, bit order and timing
in `encoder`, clock recovery (`recovery_test.go`, with a synthetic sender whose
clock is off, drifts and jitters) and event handling in `decoder`.

```sh
go test ./manchester/...
go test -race ./manchester/...
go test ./manchester/decoder/ -run TestRecovery -v
```

Coverage is 89.9 % of statements in `encoder` and 91.5 % in `decoder`. The
roundtrip test in this directory reports no statements of its own — it is pure
integration.

## Not addressed

Known and deliberately left alone:

- **No byte reassembly on the receiving side.** The encoder frames bytes with a
  start and a stop bit, the decoder does not unframe them —
  `demo/manchester_listener` carries that code, and every consumer repeats it.
- **No integrity check.** No CRC, no checksum, no length field. A corrupted bit
  is delivered as a bit, and only `Invalid` marks a *timing* violation.
- **No idle level.** `Close` leaves the line wherever the last half-bit put it,
  see [Timing](#timing).
- **Dropped bits are counted, not reported.** A full `Bits()` channel loses bits
  silently; `Info()` is the only way to notice.
- **The encoder has no logger**, by design — it reports through
  `WithErrorHandler` instead, and only for `SetValue` failures.
- **Clock recovery needs both interval lengths.** A stream of only identical
  bits, or of only alternating bits, never locks, and bits buffered beyond 512
  edges are dropped from the front. Every real frame format mixes both;
  otherwise pass the bit clock.
- **No polarity detection.** The decoder cannot know which bits a sender meant
  — an inverted line decodes as the complement. Detecting it needs knowledge of
  the frame format, so it is left to the caller.

Full API: `go doc github.com/womat/golib/manchester/encoder` and
`.../manchester/decoder`.
