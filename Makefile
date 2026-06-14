BIN      := ccquota
CMD      := ./cmd/ccquota
LDFLAGS  := -ldflags "-s -w"

.PHONY: build test vet fmt run docker

## build: compile the binary (native arch)
build:
	CGO_ENABLED=0 go build $(LDFLAGS) -o $(BIN) $(CMD)

## test: run all tests
test:
	go test ./... -count=1

## vet: run go vet
vet:
	go vet ./...

## fmt: format source and report diffs
fmt:
	gofmt -l -w .

## run: build + start the server on :8080
run: build
	./$(BIN) serve --addr :8080

## docker: build multi-arch image tagged ccquota:dev
docker:
	docker buildx build \
		--platform linux/amd64,linux/arm64 \
		-t ccquota:dev \
		--load \
		.
