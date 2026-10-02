BINARY := cronwatch

.PHONY: build test run fmt vet generate release clean

build:
	go build -trimpath -o bin/$(BINARY) ./cmd/cronwatch

test:
	go test ./...

fmt:
	gofmt -w $$(find . -name '*.go')

vet:
	go vet ./...

generate:
	sqlc generate

release:
	sh scripts/release.sh $(VERSION)

run:
	go run ./cmd/cronwatch serve

clean:
	rm -rf bin
