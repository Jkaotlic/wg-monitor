# wg-monitor build matrix
# Stage 0: pure Go, no cgo. Cross-compile via standard GOOS/GOARCH/GOMIPS env.

VERSION ?= 0.1.0-dev
LDFLAGS := -s -w -X main.Version=$(VERSION)
GOFLAGS := -trimpath -ldflags "$(LDFLAGS)"

BIN_DIR := bin

.PHONY: all build-host build-cli build-deploy build-mipsel build-aarch64 pack test clean size

all: build-host build-cli

# Local OS, used for go test and dev
build-host:
	mkdir -p $(BIN_DIR)
	go build $(GOFLAGS) -o $(BIN_DIR)/wg-monitor ./cmd/agent
	go build $(GOFLAGS) -o $(BIN_DIR)/wg-monitor-backend ./cmd/backend

build-cli:
	mkdir -p $(BIN_DIR)
	go build $(GOFLAGS) -o $(BIN_DIR)/wg-monitor-cli ./cmd/wg-monitor-cli

build-deploy:
	mkdir -p $(BIN_DIR)
	go build $(GOFLAGS) -o $(BIN_DIR)/wg-monitor-deploy ./cmd/deploy

# Keenetic with MIPS little-endian, no FPU (most common: Realtek/Qualcomm SoCs)
build-mipsel:
	mkdir -p $(BIN_DIR)/linux-mipsle
	GOOS=linux GOARCH=mipsle GOMIPS=softfloat go build $(GOFLAGS) -o $(BIN_DIR)/linux-mipsle/wg-monitor ./cmd/agent

# Keenetic with ARM64 (newer flagship models like Hopper, Peak)
build-aarch64:
	mkdir -p $(BIN_DIR)/linux-arm64
	GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -o $(BIN_DIR)/linux-arm64/wg-monitor ./cmd/agent

# UPX-pack cross-compiled agent binaries.
# UPX must be in PATH. Install: choco install upx (Win) or apt install upx-ucl (Linux).
pack: build-mipsel build-aarch64
	upx --best --lzma $(BIN_DIR)/linux-mipsle/wg-monitor
	upx --best --lzma $(BIN_DIR)/linux-arm64/wg-monitor
	@$(MAKE) size

# Verify packed binaries are under target sizes.
# Spec target: 1.5–3 MB. We hard-fail if either exceeds 3 MiB.
size:
	@for f in $(BIN_DIR)/linux-mipsle/wg-monitor $(BIN_DIR)/linux-arm64/wg-monitor; do \
		if [ -f "$$f" ]; then \
			bytes=$$(wc -c < "$$f"); \
			mib=$$(awk "BEGIN{printf \"%.2f\", $$bytes/1048576}"); \
			echo "$$f: $$bytes bytes ($$mib MiB)"; \
			if [ $$bytes -gt 3145728 ]; then echo "FAIL: $$f exceeds 3 MiB"; exit 1; fi; \
		fi; \
	done

test:
	go test ./...

clean:
	rm -rf $(BIN_DIR)
