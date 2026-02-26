# Makefile für Raspberry Pi Deployment

# Name deiner Binary
BINARY := bin/manchester_listener
MAIN := cmd/main.go

# Raspberry Pi Login / IP
PI_USER := pv
PI_HOST := pi400ssd
PI_PATH := /home/pv/

# Cross-Compile Settings für Raspberry Pi (ARMv6/7/8)
GOOS := linux
GOARCH := arm
GOARM := 7   # 6 für Pi 0/1, 7 für Pi 2/3/4


# Standard Build
all: build

# Binary bauen
build:
	@echo "Building for Raspberry Pi..."
	GOOS=$(GOOS) GOARCH=$(GOARCH) GOARM=$(GOARM) go build -o $(BINARY)  $(MAIN)

# SCP Upload
deploy: build
	@echo "Copying binary to Raspberry Pi..."
	scp $(BINARY) $(PI_USER)@$(PI_HOST):$(PI_PATH)

# Clean
clean:
	rm -f $(BINARY)