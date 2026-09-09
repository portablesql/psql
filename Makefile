#!/usr/bin/make

.PHONY: all build deps test fmt vet lint

all: fmt build

build:
	go build -v ./...

deps:
	go get -v -t ./...

test:
	go test -race ./...

fmt:
	gofmt -s -w -l .

vet:
	go vet ./...

lint: vet
	@if command -v staticcheck >/dev/null 2>&1; then staticcheck ./...; else echo "staticcheck not installed (go install honnef.co/go/tools/cmd/staticcheck@latest)"; fi
