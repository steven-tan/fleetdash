VERSION := $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: run test vet build dist clean

run:   ## run locally on :8080 (set FLEETDASH_* to taste)
	go run -ldflags "$(LDFLAGS)" .

test:
	go test ./...

vet:
	go vet ./...

build: ## local binary for this OS/arch
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o fleetdash .

dist:  ## the two linux binaries the pipeline produces
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/fleetdash-linux-amd64 .
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/fleetdash-linux-arm64 .

clean:
	rm -rf fleetdash dist
