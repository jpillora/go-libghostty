#!/usr/bin/env bash
# Build libghostty-vt, build the Go bindings, and run every example.
#
# Prereqs already on PATH: go, zig, cmake, gcc, ld, pkg-config.
set -euo pipefail

# Always run from the repo root, regardless of caller's cwd.
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$REPO_ROOT"

# --- 1. System deps.
# ghostty's static link lists `-lc++` in libghostty-vt.pc.
# Symbola and Unifont provide fallback glyphs (e.g. U+23F5 ⏵) that
# neither JetBrains Mono nor Symbols Nerd Font carry.
PKGS=()
ldconfig -p | grep -q 'libc++\.so'       || PKGS+=(libc++-dev libc++abi-dev)
dpkg -s fonts-symbola &>/dev/null         || PKGS+=(fonts-symbola)
dpkg -s fonts-unifont &>/dev/null         || PKGS+=(fonts-unifont)
if (( ${#PKGS[@]} )); then
  echo "==> Installing ${PKGS[*]}"
  sudo apt-get install -y "${PKGS[@]}"
fi

# --- 2. Zig global cache workaround.
# On this machine ~/.cache is a symlink to /mnt/data/cache, so Zig's build
# steps that chdir into a cached package (e.g. uucode) and then execve a
# helper exe via a relative `../../../../projects/golibvt/...` path fail
# with ENOENT, because `..` resolves from the physical /mnt/data/cache
# path and overshoots /home/ubuntu. Point the cache under the project
# tree so source and artifacts share the same physical root.
export ZIG_GLOBAL_CACHE_DIR="$REPO_ROOT/.zig-global-cache"

# --- 3. Build libghostty-vt (via CMake+Zig) and the Go module.
# `make build` runs cmake -> zig build -> `go build ./...`.
echo "==> make build"
make build

# --- 4. Point cgo's pkg-config at the freshly built libghostty-vt.
export PKG_CONFIG_PATH="$REPO_ROOT/build/_deps/ghostty-src/zig-out/share/pkgconfig"

# --- 5. Run each example.
EXAMPLES=(build-info colors effects formatter grid-traverse modes render png)
for ex in "${EXAMPLES[@]}"; do
  echo
  echo "=============================="
  echo "EXAMPLE: $ex"
  echo "=============================="
  go run "./examples/$ex"
done

# --- 6. Render the `claude` welcome screen to PNG via render-cmd.
# Uses ghostty's bundled JetBrains Mono as primary font, with
# Symbols Nerd Font + Symbola + Unifont as glyph fallback chain.
echo
echo "=============================="
echo "RENDER: claude → PNG"
echo "=============================="
go run ./examples/render-cmd \
  -o "$REPO_ROOT/claude.png" \
  -cols 100 -rows 30 \
  -idle 1500ms -deadline 15s \
  claude
echo "  → $REPO_ROOT/claude.png"
