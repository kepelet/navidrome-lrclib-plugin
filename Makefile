SHELL := /usr/bin/env bash
.PHONY: build package clean

PLUGIN_NAME := navidrome-lrclib
WASM_FILE   := plugin.wasm
TINYGO      := $(shell command -v tinygo 2>/dev/null)

build:
ifdef TINYGO
	tinygo build -opt=2 -scheduler=none -no-debug -o $(WASM_FILE) -target wasi -buildmode=c-shared .
else
	GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o $(WASM_FILE) .
endif

package: build
	zip $(PLUGIN_NAME).ndp $(WASM_FILE) manifest.json

clean:
	rm -f $(WASM_FILE) $(PLUGIN_NAME).ndp
