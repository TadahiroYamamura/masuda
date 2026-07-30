.PHONY: build vet test test-go test-py install docker-images

build:
	go build -o masuda ./cmd/masuda

vet:
	go vet ./...

test-go:
	go test ./...

test-py:
	venv/bin/pytest orchestrator/tests/

test: test-go test-py

install: build
	install -m 0755 masuda /usr/local/bin/masuda

docker-images:
	docker build -t masuda-loop:latest .
	docker build -f docker/go/Dockerfile         -t masuda-loop:go         .
	docker build -f docker/python/Dockerfile     -t masuda-loop:python     .
	docker build -f docker/typescript/Dockerfile -t masuda-loop:typescript .
	docker build -f docker/full/Dockerfile       -t masuda-loop:full       .
