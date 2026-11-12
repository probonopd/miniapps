VERSION ?= 0.1.0
LDFLAGS = -s -w
TARGETS = linux/arm64 linux/arm linux/amd64 linux/386 freebsd/amd64 freebsd/arm64 openbsd/amd64 openbsd/arm64

.PHONY: build all test vet clean loadgen resource-test

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o miniha ./cmd/miniha

all:
	@mkdir -p dist
	@for t in $(TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; \
		echo "building $$os/$$arch"; \
		GOOS=$$os GOARCH=$$arch GOARM=7 CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/miniha-$$os-$$arch ./cmd/miniha || exit 1; \
	done

test:
	go test ./...

vet:
	go vet ./... && test -z "$$(gofmt -l cmd internal web)"

clean:
	rm -rf miniha dist

# Synthetic MQTT load for the SPEC section 10 budget; output stays in dist/, which is ignored.
loadgen:
	@mkdir -p dist
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/loadgen ./cmd/loadgen

resource-test:
	sh scripts/resource-test.sh
