BUILD_DIR := build

# FetchContent places the ghostty source here.
GHOSTTY_ZIG_OUT := $(CURDIR)/$(BUILD_DIR)/_deps/ghostty-src/zig-out
PKG_CONFIG_PATH := $(GHOSTTY_ZIG_OUT)/share/pkgconfig
DYLD_LIBRARY_PATH := $(GHOSTTY_ZIG_OUT)/lib
LD_LIBRARY_PATH := $(GHOSTTY_ZIG_OUT)/lib

# Stamp file to track whether the cmake build has run.
STAMP := $(BUILD_DIR)/.ghostty-built

# Cross-compilation target definitions.
# Each entry maps a make target suffix to GOOS, GOARCH, zig target triple,
# and the CC/CXX target flag for zig cc.
CROSS_TARGETS := linux-amd64 linux-arm64 macos-amd64 macos-arm64 windows-amd64 windows-arm64

linux-amd64_GOOS   := linux
linux-amd64_GOARCH := amd64
linux-amd64_ZIG    := x86_64-linux-gnu

linux-arm64_GOOS   := linux
linux-arm64_GOARCH := arm64
linux-arm64_ZIG    := aarch64-linux-gnu

macos-amd64_GOOS   := darwin
macos-amd64_GOARCH := amd64
macos-amd64_ZIG    := x86_64-macos

macos-arm64_GOOS   := darwin
macos-arm64_GOARCH := arm64
macos-arm64_ZIG    := aarch64-macos

windows-amd64_GOOS   := windows
windows-amd64_GOARCH := amd64
windows-amd64_ZIG    := x86_64-windows-gnu

windows-arm64_GOOS   := windows
windows-arm64_GOARCH := arm64
windows-arm64_ZIG    := aarch64-windows-gnu

.PHONY: build test clean cross $(addprefix cross-,$(CROSS_TARGETS))

$(STAMP):
	cmake -B $(BUILD_DIR) -DCMAKE_BUILD_TYPE=Release
	cmake --build $(BUILD_DIR)
	@touch $(STAMP)

build: $(STAMP)
	PKG_CONFIG_PATH=$(PKG_CONFIG_PATH) go build ./...

test: $(STAMP)
	PKG_CONFIG_PATH=$(PKG_CONFIG_PATH) DYLD_LIBRARY_PATH=$(DYLD_LIBRARY_PATH) LD_LIBRARY_PATH=$(LD_LIBRARY_PATH) go test ./...

# cross builds all cross-compilation targets.
cross: $(addprefix cross-,$(CROSS_TARGETS))

# cross-<target> cross-compiles the Go package for the given target using
# zig cc and the libghostty-vt static library built by CMake.
define CROSS_RULE
cross-$(1): $(STAMP)
	CGO_ENABLED=1 \
	CC="zig cc -target $$($(1)_ZIG)" \
	CXX="zig c++ -target $$($(1)_ZIG)" \
	GOOS=$$($(1)_GOOS) \
	GOARCH=$$($(1)_GOARCH) \
	CGO_CFLAGS="-I$(CURDIR)/$(BUILD_DIR)/ghostty-$(1)/include -DGHOSTTY_STATIC" \
	CGO_LDFLAGS="-L$(CURDIR)/$(BUILD_DIR)/ghostty-$(1)/lib -lghostty-vt" \
	go build . ./sys/...
endef

$(foreach t,$(CROSS_TARGETS),$(eval $(call CROSS_RULE,$(t))))

clean:
	rm -rf $(BUILD_DIR)
