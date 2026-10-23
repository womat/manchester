# Makefile for building and deploying to a Raspberry Pi

# Name of the binary
BINARY := bin/manchester_sender
MAIN := cmd/main.go

# Raspberry Pi login and host
PI_USER := pv
PI_HOST := pi400ssd
PI_PATH := /home/pv/

# Cross-compile settings for a Raspberry Pi (ARMv6/7/8)
GOOS := linux
GOARCH := arm
GOARM := 7   # 6 for Pi 0/1, 7 for Pi 2/3/4


# Default target
all: build

# Build the binary
build:
	@echo "Building for Raspberry Pi..."
	GOOS=$(GOOS) GOARCH=$(GOARCH) GOARM=$(GOARM) go build -o $(BINARY)  $(MAIN)

# Copy to the Raspberry Pi with scp
deploy: build
	@echo "Copying binary to Raspberry Pi..."
	scp $(BINARY) $(PI_USER)@$(PI_HOST):$(PI_PATH)

# Clean
clean:
	rm -f $(BINARY)