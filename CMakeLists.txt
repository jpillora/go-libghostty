cmake_minimum_required(VERSION 3.19)
project(go-libghostty LANGUAGES C)

include(FetchContent)
FetchContent_Declare(ghostty
    GIT_REPOSITORY https://github.com/ghostty-org/ghostty.git
    GIT_TAG 2ed382a15566b267c32fae440b065f7844b15bfb
)
FetchContent_MakeAvailable(ghostty)

# Cross-compilation targets for CI and multi-platform builds.
# Each call produces ghostty-vt-static-<NAME> and ghostty-vt-<NAME>
# IMPORTED targets whose output lands under build/ghostty-<NAME>/.
ghostty_vt_add_target(NAME linux-amd64    ZIG_TARGET x86_64-linux-gnu)
ghostty_vt_add_target(NAME linux-arm64    ZIG_TARGET aarch64-linux-gnu)
ghostty_vt_add_target(NAME macos-amd64    ZIG_TARGET x86_64-macos)
ghostty_vt_add_target(NAME macos-arm64    ZIG_TARGET aarch64-macos)
ghostty_vt_add_target(NAME windows-amd64  ZIG_TARGET x86_64-windows-gnu)
ghostty_vt_add_target(NAME windows-arm64  ZIG_TARGET aarch64-windows-gnu)
