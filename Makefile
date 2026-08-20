.PHONY: build vet fmt fmt-check test test-go test-py install docker-images reviews-zip

PREFIX ?= /usr/local
# VERSION is embedded via ldflags (ADR-0032) the same way CI does
# (-X main.version=<tag>). Defaults to "dev", matching main.go's own
# fallback, so a plain `make build` behaves as before. Pass a real
# published tag (e.g. `make build VERSION=v0.0.1`) to locally reproduce
# that release's `masuda --version` -- this does NOT let you test local
# edits to internal/perspectives/builtin/*.md or the root Dockerfile,
# since `masuda init`/`masuda update` always fetch that content from the
# matching GitHub Release (ADR-0033), never from the local checkout.
VERSION ?= dev

build:
	go build -ldflags "-X main.version=$(VERSION)" -o masuda ./cmd/masuda

vet:
	go vet ./...

fmt:
	gofmt -w .

fmt-check:
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)

test-go:
	go test ./...

test-py:
	venv/bin/pytest orchestrator/tests/

test: test-go test-py

install: build
	install -m 0755 masuda $(PREFIX)/bin/masuda

docker-images:
	docker build -t masuda-loop:latest .

# Reproduces .github/workflows/release.yml's `reviews` job locally (ADR-0033)
# -- zips internal/perspectives/builtin/*.md the same way CI does, so you
# can inspect the exact artifact a release would attach without pushing a
# tag.
reviews-zip:
	rm -f masuda_reviews.zip
	cd internal/perspectives/builtin && zip -r ../../../masuda_reviews.zip .
