INSTALL_PREFIX ?= /usr/local

.PHONY: build test install uninstall clean

build:
	go build -o ghq ./cmd/ghq

test:
	go test ./...

install: build
	install -d $(INSTALL_PREFIX)/bin
	install -m 755 ghq $(INSTALL_PREFIX)/bin/ghq

uninstall:
	rm -f $(INSTALL_PREFIX)/bin/ghq

clean:
	rm -f ghq
