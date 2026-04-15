# Version management - single source of truth
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE := $(shell date -u +'%Y-%m-%dT%H:%M:%SZ')

APP=gateway-target-prober
PKG=ghcr.io/b1r3k/gateway-target-prober
GHCR_REPO_URI=ghcr.io
GHCR_REPO_USER=b1r3k

# Build flags for version injection
LDFLAGS := -ldflags="-X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)"

.PHONY: test fmt vet tidy clean build version info

ghcr-login:
	keyring get $(APP) ghcr_registry | docker login $(GHCR_REPO_URI) --username $(GHCR_REPO_USER) --password-stdin

build: tidy fmt vet test
	go build $(LDFLAGS) -o bin/$(APP) ./main.go

run:
	go run ./cmd/$(APP)

test:
	go test ./...

fmt:
	go fmt ./...

vet:
	go vet ./...

tidy:
	go mod tidy

clean:
	rm -rf bin

build-image: ghcr-login
	docker buildx build \
		--platform linux/arm64,linux/amd64 \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg DATE=$(DATE) \
		-t ghcr.io/b1r3k/gateway-target-prober:$(VERSION) \
		-t ghcr.io/b1r3k/gateway-target-prober:latest \
		--push .

# Version management targets
version:
	@echo $(VERSION)

info:
	@echo "Version: $(VERSION)"
	@echo "Commit:  $(COMMIT)"
	@echo "Date:    $(DATE)"
	@echo "App:     $(APP)"
	@echo "Package: $(PKG)"
