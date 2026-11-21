#!/bin/sh
# Builds single file, dependency free executables into dist/
#
# Windows builds are NOT stripped (-s -w): stripped, unsigned Go binaries are
# flagged by antivirus heuristics far more often. Version info and the icon
# come from rsrc_windows_*.syso (regenerate with go-winres, see README).
set -e
cd "$(dirname "$0")"
mkdir -p dist
export CGO_ENABLED=0
GOOS=windows GOARCH=amd64 go build -trimpath -o dist/radio-bandwidth.exe .
GOOS=windows GOARCH=arm64 go build -trimpath -o dist/radio-bandwidth-windows-arm64.exe .
S="-trimpath -ldflags=-s -ldflags=-w"
GOOS=linux   GOARCH=amd64 go build $S -o dist/radio-bandwidth-linux-amd64 .
GOOS=linux   GOARCH=arm64 go build $S -o dist/radio-bandwidth-linux-arm64 .
GOOS=darwin  GOARCH=arm64 go build $S -o dist/radio-bandwidth-mac-arm64 .
GOOS=darwin  GOARCH=amd64 go build $S -o dist/radio-bandwidth-mac-intel .
# macOS: Radio Bandwidth.app with one universal program, zipped
VERSION="$(sed -n 's/^var appVersion = "\(.*\)"$/\1/p' main.go)"
go run ./tools/mkmacapp -version "$VERSION" -icon winres/icon.png \
  -arm64 dist/radio-bandwidth-mac-arm64 -amd64 dist/radio-bandwidth-mac-intel \
  -universal dist/radio-bandwidth-mac-universal -out dist/RadioBandwidth-mac.zip
cp "Radio Bandwidth second copy.bat" dist/
ls -l dist
