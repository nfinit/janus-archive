# JANUS — Makefile
#
# Goals: clone-and-forget. `make && make install` produces and installs
# static binaries with zero runtime dependencies on the target.

GO        ?= go
PREFIX    ?= /usr/local
BINDIR     = $(PREFIX)/bin
BUILDDIR   = build

# Static binaries: no cgo, no external linker.
GOFLAGS    = CGO_ENABLED=0
LDFLAGS    = -ldflags "-s -w"

.PHONY: all server client test vet clean install uninstall vendor

all: server client

server:
	$(GOFLAGS) $(GO) build $(LDFLAGS) -o $(BUILDDIR)/janus-server ./cmd/janus-server

client:
	$(GOFLAGS) $(GO) build $(LDFLAGS) -o $(BUILDDIR)/janus-client ./cmd/janus-client

test:
	$(GOFLAGS) $(GO) test ./...

vet:
	$(GO) vet ./...

install: all
	install -d $(DESTDIR)$(BINDIR)
	install -m 0755 $(BUILDDIR)/janus-server $(DESTDIR)$(BINDIR)/janus-server
	install -m 0755 $(BUILDDIR)/janus-client $(DESTDIR)$(BINDIR)/janus-client

uninstall:
	rm -f $(DESTDIR)$(BINDIR)/janus-server $(DESTDIR)$(BINDIR)/janus-client

# Refresh the vendored module tree (committed for fully offline builds).
vendor:
	$(GO) mod tidy
	$(GO) mod vendor

clean:
	rm -rf $(BUILDDIR)
