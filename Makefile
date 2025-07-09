PACKAGE_NAME          := github.com/imua-xyz/price-feeder
GOLANG_CROSS_VERSION  = v1.22-v2.0.0
GOPATH ?= '$(HOME)/go'

VERSION  := $(shell git describe --tags 2>/dev/null || echo "v0.0.0-unknown")
COMMIT   := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

LDFLAGS := -X github.com/imua-xyz/price-feeder/version.Version=$(VERSION) \
           -X github.com/imua-xyz/price-feeder/version.Commit=$(COMMIT) \
           -X github.com/imua-xyz/price-feeder/version.BuildDate=$(BUILD_DATE)

release-dry-run:
	docker run \
		-e GOPROXY=https://proxy.golang.org,direct \
		--rm \
		--privileged \
		-e CGO_ENABLED=1 \
		-v /var/run/docker.sock:/var/run/docker.sock \
		-v `pwd`:/go/src/$(PACKAGE_NAME) \
		-v ${GOPATH}/pkg:/go/pkg \
		-w /go/src/$(PACKAGE_NAME) \
		ghcr.io/goreleaser/goreleaser-cross:${GOLANG_CROSS_VERSION} \
		--clean --skip validate,publish --snapshot

release:
	@if [ ! -f ".release-env" ]; then \
		echo "\033[91m.release-env is required for release\033[0m";\
		exit 1;\
	fi
	docker run \
		--rm \
		-e CGO_ENABLED=1 \
		--env-file .release-env \
		-v /var/run/docker.sock:/var/run/docker.sock \
		-v `pwd`:/go/src/$(PACKAGE_NAME) \
		-v `pwd`/sysroot:/sysroot \
		-w /go/src/$(PACKAGE_NAME) \
		ghcr.io/goreleaser/goreleaser-cross:${GOLANG_CROSS_VERSION} \
		release --clean --skip validate

build:
	go build -ldflags "$(LDFLAGS)" -o ./build/price-feeder

build-devmode:
	go build -ldflags "$(LDFLAGS)" --tags devmode -o ./build/price-feeder

proto-gen:
	cd proto && buf generate
	cp -r github.com/imua-xyz/price-feeder/internal/* ./internal/
	rm -rf github.com

.PHONY: build
