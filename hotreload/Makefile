.PHONY: build test vet check demo testserver clean

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT)

BIN := hotreload
SERVER := server
ifeq ($(OS),Windows_NT)
	BIN := hotreload.exe
	SERVER := server.exe
endif

build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN) .

test:
	go test ./...

vet:
	go vet ./...

check: vet test

demo: build testserver
	@echo "Running demo (hotreload watching testserver)..."
	./$(BIN) --root ./testserver \
		--build "go build -o $(SERVER) ." \
		--exec "./$(SERVER)"

testserver:
	cd testserver && go mod tidy && go build -o $(SERVER) .

clean:
	rm -f $(BIN) hotreload hotreload.exe
	rm -f testserver/server testserver/server.exe testserver/testserver.exe
	rm -rf tmp
