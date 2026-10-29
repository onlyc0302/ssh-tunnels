#!/bin/sh
# Invoked by ./build.sh release-linux after the Go environment is configured.
set -eu
export LC_ALL=C
release_version=$(awk '/^const version = / {gsub(/"/, "", $4); print $4}' main.go)
case "$release_version" in ''|*[!0-9.]*) echo 'Invalid release version' >&2; exit 1 ;; esac
mkdir -p dist
stage=$(mktemp -d dist/.linux-release.XXXXXX)
trap 'rm -rf "$stage"' 0
trap 'exit 1' HUP INT TERM

go mod download
go run ./tools/notices > "$stage/THIRD_PARTY_NOTICES.txt"
for arch in amd64 arm64; do
  name="kitty-tunnels_${release_version}_linux_${arch}"
  package="$stage/$name"
  mkdir -p "$package"
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" GOAMD64=v1 GOARM64=v8.0 \
    go build -trimpath -ldflags='-s -w' -o "$package/tunnels" .
  cp README.md README-LINUX.md RELEASE-NOTES.md "$stage/THIRD_PARTY_NOTICES.txt" "$package/"
  COPYFILE_DISABLE=1 tar -czf "$stage/$name.tar.gz" -C "$stage" "$name"
done
(
  cd "$stage"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum ./*.tar.gz > SHA256SUMS
  else
    shasum -a 256 ./*.tar.gz > SHA256SUMS
  fi
)
for artifact in "$stage"/*.tar.gz "$stage/SHA256SUMS"; do
  mv -f "$artifact" dist/
done
echo "Linux / WSL $release_version packages: $PWD/dist"
