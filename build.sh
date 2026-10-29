#!/bin/sh
set -eu

# Resolve the selected Go binary instead of inheriting a stale global GOROOT.
go_bin=$(command -v go)
while [ -L "$go_bin" ]; do
  target=$(readlink "$go_bin")
  case "$target" in
    /*) go_bin=$target ;;
    *) go_bin=$(dirname "$go_bin")/$target ;;
  esac
done
go_root=$(cd "$(dirname "$go_bin")/.." && pwd -P)
if [ -d "$go_root/src" ]; then
  export GOROOT="$go_root"
fi
export GOPROXY="${TUNNELS_GOPROXY:-https://proxy.golang.org,direct}"
export GOSUMDB="${TUNNELS_GOSUMDB:-sum.golang.org}"
cd "$(dirname "$0")"

case "${1:-build}" in
  build)
    mkdir -p bin
    # Build a fresh inode, then atomically publish it. Never overwrite a running
    # executable or inherit attributes added to an older distributed binary.
    build_dir=$(mktemp -d bin/.build.XXXXXX)
    trap 'rm -rf "$build_dir"' 0
    trap 'exit 1' HUP INT TERM
    go build -trimpath -o "$build_dir/tunnels" .
    mv -f "$build_dir/tunnels" bin/tunnels
    ;;
  test) go test -race -v ./... ;;
  vet) go vet ./... ;;
  release-linux) sh scripts/release-linux.sh ;;
  test-binaries-linux)
    mkdir -p release-tests
    for arch in amd64 arm64; do
      CGO_ENABLED=0 GOOS=linux GOARCH="$arch" GOAMD64=v1 GOARM64=v8.0 \
        go test -c -o "release-tests/tunnels_linux_$arch.test" .
    done
    ;;
  *) echo '用法: ./build.sh [build|test|vet|release-linux|test-binaries-linux]' >&2; exit 2 ;;
esac
