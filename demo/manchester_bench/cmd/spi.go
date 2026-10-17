package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// spiTransfer mirrors struct spi_ioc_transfer from linux/spi/spidev.h.
type spiTransfer struct {
	txBuf, rxBuf  uint64
	length        uint32
	speedHz       uint32
	delayUsecs    uint16
	bitsPerWord   uint8
	csChange      uint8
	txNbits       uint8
	rxNbits       uint8
	wordDelayUsec uint8
	pad           uint8
}

const spiIOCMessage1 = 0x40206b00 // _IOW('k', 0, char[32])

// halfBits splits a message into half bits the way encoder.Send does (IEEE,
// LSB first, start and stop bit per byte).
func halfBits(msg []byte, syncBytes int) []byte {
	var hb []byte
	bit := func(b byte) {
		if b == 1 {
			hb = append(hb, 0, 1) // IEEE: 1 = low → high
		} else {
			hb = append(hb, 1, 0)
		}
	}
	for range syncBytes {
		for range 8 {
			bit(1)
		}
	}
	for _, b := range msg {
		bit(0)
		for i := range 8 {
			bit((b >> i) & 1)
		}
		bit(1)
	}
	return hb
}

// spiBytes stretches every half bit to k SPI bits and packs them MSB first.
// The SPI clock cannot go much below 8 kHz (the divider is 16 bits), so a few
// kbit/s only work oversampled.
func spiBytes(hb []byte, k, tail int) []byte {
	last := hb[len(hb)-1]
	for range tail { // hold the last level before MOSI goes idle
		hb = append(hb, last)
	}
	out := make([]byte, (len(hb)*k+7)/8)
	n := 0
	for _, h := range hb {
		for range k {
			if h == 1 {
				out[n/8] |= 0x80 >> (n % 8)
			}
			n++
		}
	}
	return out
}

func spiSend(args []string) {
	fs := flag.NewFlagSet("spisend", flag.ExitOnError)
	dev := fs.String("dev", "/dev/spidev0.0", "SPI device")
	rate := fs.Int("rate", 1000, "bit/s")
	k := fs.Int("k", 10, "SPI bits per half bit")
	n := fs.Int("n", 50, "frames")
	size := fs.Int("len", 56, "bytes per frame")
	gap := fs.Duration("gap", 50*time.Millisecond, "pause between frames")
	delay := fs.Duration("delay", time.Second, "wait before the first frame")
	_ = fs.Parse(args)

	f, err := os.OpenFile(*dev, os.O_RDWR, 0)
	if err != nil {
		fail(err)
	}
	defer func() { _ = f.Close() }()
	speed := uint32(2 * *rate * *k)

	time.Sleep(*delay)
	start := time.Now()
	for i := range *n {
		buf := spiBytes(halfBits(payload(i, *size), 2), *k, 4)
		if len(buf) > 4096 {
			fail(fmt.Errorf("frame %d: %d bytes, spidev allows 4096", i, len(buf)))
		}
		tr := spiTransfer{
			txBuf:       uint64(uintptr(unsafe.Pointer(&buf[0]))),
			length:      uint32(len(buf)),
			speedHz:     speed,
			bitsPerWord: 8,
		}
		if _, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(), spiIOCMessage1, uintptr(unsafe.Pointer(&tr))); errno != 0 {
			fail(fmt.Errorf("SPI_IOC_MESSAGE: %w", errno))
		}
		runtime.KeepAlive(buf)
		time.Sleep(*gap)
	}
	fmt.Printf("spisend: %d frames in %v, SPI %d Hz, %d bits per half bit\n",
		*n, time.Since(start).Round(time.Millisecond), speed, *k)
}
