INSTALL_PREFIX ?= /usr/local

.PHONY: build test install uninstall clean

build:
	go build -o gg ./cmd/gg

test:
	go test ./...

install: build
	install -d $(INSTALL_PREFIX)/bin
	install -m 755 gg $(INSTALL_PREFIX)/bin/gg

uninstall:
	rm -f $(INSTALL_PREFIX)/bin/gg

clean:
	rm -f gg
