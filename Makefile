# Builds every sample into dist/, in the form Sona installs: one folder per
# add-on, named by its ID, with addon.json and the program.
#
#   make              build everything
#   make test         go test -race, and the manifest checker on dist/
#   make wasm         only the WebAssembly build of copy-songlink
#   make install ID=copy-songlink   copy a built sample into Sona's add-ons folder

GO       ?= go
DIST     ?= dist
GO_IDS   := now-playing-webhook home-assistant-bridge copy-songlink
WASM_IDS := copy-songlink
PY_IDS   := reference-client
LDFLAGS  := -s -w

all: native wasm python

# Native Go add-ons: static binaries, so they need nothing from the system but
# the kernel (and, for the network ones, certificates).
native:
	@for id in $(GO_IDS); do \
	  echo "build native $$id"; \
	  install -m 0755 -d $(DIST)/native/$$id/bin && \
	  CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(DIST)/native/$$id/bin/$$id ./$$id && \
	  install -m 0644 $$id/addon.json $(DIST)/native/$$id/addon.json || exit 1; \
	done

# The same source as the native build, for WASI: a wasm add-on has no network
# and no permission to ask for it, so only add-ons that need neither belong here.
wasm:
	@for id in $(WASM_IDS); do \
	  echo "build wasip1/wasm $$id"; \
	  install -m 0755 -d $(DIST)/wasm/$$id && \
	  GOOS=wasip1 GOARCH=wasm $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(DIST)/wasm/$$id/$$id.wasm ./$$id && \
	  install -m 0644 $$id/addon.wasm.json $(DIST)/wasm/$$id/addon.json || exit 1; \
	done

# Scripts need no build: they are copied as they are.
python:
	@for id in $(PY_IDS); do \
	  echo "copy $$id"; \
	  install -m 0755 -d $(DIST)/native/$$id && \
	  install -m 0755 $$id/addon.py $(DIST)/native/$$id/addon.py && \
	  install -m 0644 $$id/addon.json $(DIST)/native/$$id/addon.json || exit 1; \
	done

test: all
	$(GO) vet ./...
	$(GO) test -race ./...
	$(GO) run ./tools/checkmanifest $(DIST)/native/* $(DIST)/wasm/*

install: all
	@test -n "$(ID)" || { echo "usage: make install ID=<add-on id>"; exit 2; }
	./scripts/install.sh $(DIST)/native/$(ID)

clean:
	rm -rf $(DIST)

.PHONY: all native wasm python test install clean
