SHELL := /bin/bash
VERSION ?= 0.1.0
GO_ENV := GOCACHE=/tmp/quotadeck-go-cache GOMODCACHE=/tmp/quotadeck-go-mod
DESKTOP_GO_ENV := GOCACHE=/tmp/quotadeck-go-cache
WAILS ?= $(shell go env GOPATH)/bin/wails
PKG_CONFIG_ENV := PKG_CONFIG=/usr/bin/pkg-config PKG_CONFIG_PATH=/usr/lib/x86_64-linux-gnu/pkgconfig:/usr/share/pkgconfig

.PHONY: dev demo web-build docs-dev docs-build test test-race lint build desktop-icon desktop-build package-cinnamon package-appimage package-deb package-release clean

dev:
	npm --prefix web run dev & web_pid=$$!; trap 'kill $$web_pid 2>/dev/null || true' EXIT; $(GO_ENV) go run ./cmd/quotadeck serve

demo:
	$(GO_ENV) go run ./tools/demo-server

web-build:
	npm --prefix web run build

docs-dev:
	npm --prefix docs run dev

docs-build:
	npm --prefix docs run build

test:
	npm --prefix web run typecheck
	$(GO_ENV) go test ./...

test-race:
	$(GO_ENV) go test -race ./...

lint:
	test -z "$$(gofmt -l cmd internal)"
	$(GO_ENV) go vet ./...
	npm --prefix web run typecheck

build: web-build
	mkdir -p dist
	$(GO_ENV) CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o dist/quotadeck ./cmd/quotadeck

desktop-icon:
	convert -background none packaging/desktop/quotadeck.svg -resize 512x512 cmd/quotadeck-desktop/appicon.png

desktop-build: web-build desktop-icon
	mkdir -p dist
	cd cmd/quotadeck-desktop && $(DESKTOP_GO_ENV) $(PKG_CONFIG_ENV) $(WAILS) build -tags "desktop,webkit2_41" -platform linux/amd64 -trimpath -skipbindings -s -m -nosyncgomod -ldflags "-s -w -X main.version=$(VERSION)" -o quotadeck-desktop-linux-amd64
	install -m 0755 cmd/quotadeck-desktop/build/bin/quotadeck-desktop-linux-amd64 dist/quotadeck-desktop-linux-amd64

package-cinnamon:
	VERSION=$(VERSION) ./scripts/package-cinnamon.sh

package-appimage: desktop-build
	VERSION=$(VERSION) ./scripts/desktop/build-appimage.sh amd64

package-deb: build desktop-build package-cinnamon
	VERSION=$(VERSION) ./scripts/package-deb.sh

package-release: build desktop-build package-cinnamon
	VERSION=$(VERSION) ./scripts/package-deb.sh
	VERSION=$(VERSION) ./scripts/desktop/build-appimage.sh amd64
	VERSION=$(VERSION) ./scripts/package-release.sh

clean:
	rm -rf dist web/node_modules docs/node_modules docs/.vitepress/cache docs/.vitepress/dist internal/httpapi/ui/assets
