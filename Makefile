GO      ?= go
PREFIX  ?= /usr/local
BINDIR  ?= $(PREFIX)/bin
BIN     := mazama

.PHONY: all build run test vet fmt install uninstall clean

all: build

build:
	$(GO) build -o $(BIN) .

run: build
	./$(BIN) $(ARGS)

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -w .

install: build
	mkdir -p $(DESTDIR)$(BINDIR)
	install -m 755 $(BIN) $(DESTDIR)$(BINDIR)/$(BIN)

uninstall:
	rm -f $(DESTDIR)$(BINDIR)/$(BIN)

clean:
	rm -f $(BIN)
