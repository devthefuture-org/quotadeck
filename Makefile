SHELL := /bin/bash
VERSION ?= 0.1.0
GO_ENV := GOCACHE=/tmp/quotadeck-go-cache GOMODCACHE=/tmp/quotadeck-go-mod
DESKTOP_GO ?= $(shell command -v go)
DESKTOP_HOST_PATH ?= /usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
DESKTOP_PKG_CONFIG_PATH ?= /usr/local/lib/x86_64-linux-gnu/pkgconfig:/usr/local/lib/pkgconfig:/usr/local/share/pkgconfig:/usr/lib/x86_64-linux-gnu/pkgconfig:/usr/lib/pkgconfig:/usr/share/pkgconfig

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

desktop-icon: cmd/quotadeck-desktop/appicon.png

cmd/quotadeck-desktop/appicon.png: packaging/desktop/quotadeck.svg
	convert -background none packaging/desktop/quotadeck.svg -resize 512x512 cmd/quotadeck-desktop/appicon.png

desktop-build: web-build desktop-icon
	mkdir -p dist
	env -i HOME="$(HOME)" USER="$(USER)" LOGNAME="$(LOGNAME)" LANG="$${LANG:-C.UTF-8}" PATH="$(DESKTOP_HOST_PATH)" \
		GOCACHE=/tmp/quotadeck-go-cache CGO_ENABLED=1 CC=/usr/bin/gcc CXX=/usr/bin/g++ \
		PKG_CONFIG=/usr/bin/pkg-config PKG_CONFIG_PATH="$(DESKTOP_PKG_CONFIG_PATH)" \
		"$(DESKTOP_GO)" build -buildvcs=false -trimpath -tags "desktop,production,webkit2_41" \
		-ldflags "-s -w -X main.version=$(VERSION)" -o dist/quotadeck-desktop-linux-amd64 ./cmd/quotadeck-desktop
	./scripts/check-desktop-linkage.sh dist/quotadeck-desktop-linux-amd64

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
