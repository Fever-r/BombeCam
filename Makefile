GO ?= go
OUT ?= bin/native
CMDS = ./cmd/bombecam-gateway ./cmd/bombecam-net ./cmd/bombecam-policy ./cmd/bombecam-verify ./cmd/bombecam-certgen

.PHONY: build setup test
# The gateway gets the Osaio server key and app ID from osaio-setup.txt or
# BOMBECAM_SERVER_KEY / BOMBECAM_APP_ID (make setup saves the file). The setup
# tool runs on this computer, so it is built for it even when GOOS/GOARCH ask
# for a cross build.
build:
	keyflag="$$(GOOS=$$($(GO) env GOHOSTOS) GOARCH=$$($(GO) env GOHOSTARCH) $(GO) run ./tools/setup -ldflags)" && \
	$(GO) build -buildvcs=false -trimpath -ldflags="-s -w $$keyflag" -o $(OUT)/ $(CMDS)

setup:
	$(GO) run ./tools/setup

test:
	$(GO) test -count=1 ./...
