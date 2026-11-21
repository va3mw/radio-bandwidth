#!/bin/sh
# Builds the Radio Bandwidth FlexRadio waveform (FLEX-8000 series and Aurora):
#   dist/radio-bandwidth-waveform-<version>.tar.gz
# Install it with SmartSDR's Waveform Manager. See WAVEFORM.md.
#
#   ./build-waveform.sh                # arm64, for the radio, no speed limit
#   MAX_MBPS=20 ./build-waveform.sh    # same, but testers keep traffic under 20 Mb/s
#   ARCH=amd64 ./build-waveform.sh     # for trying it with Docker on a PC
#
# The radio cannot pass options at install time, so the speed limit is built
# into the image.
set -eu
cd "$(dirname "$0")"
ARCH="${ARCH:-arm64}"
MAX_MBPS="${MAX_MBPS:-0}"
VERSION="$(sed -n 's/^var appVersion = "\(.*\)"$/\1/p' main.go)"
mkdir -p dist/waveform

CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" GOFLAGS=-buildvcs=false \
  go build -trimpath -ldflags='-s -w' -o dist/waveform/radio-bandwidth .

# Third-party licences that must travel with the binary.
{
  echo "Radio Bandwidth includes the following third-party software, under their own licences."
  # Only modules actually linked into this binary.
  for mod in $(GOOS=linux GOARCH="$ARCH" go list -deps -f '{{if .Module}}{{if not .Module.Main}}{{.Module.Path}}{{end}}{{end}}' . | sort -u); do
    dir="$(go list -m -f '{{.Dir}}' "$mod")"
    echo
    echo "================================================================"
    echo "$mod $(go list -m -f '{{.Version}}' "$mod")"
    echo "================================================================"
    cat "$dir/LICENSE"
  done
  echo
  echo "================================================================"
  echo "Go standard library $(go env GOVERSION)"
  echo "================================================================"
  cat "$(go env GOROOT)/LICENSE"
} > dist/waveform/NOTICES

OUT="dist/radio-bandwidth-waveform-$VERSION"
[ "$ARCH" = arm64 ] || OUT="$OUT-$ARCH"
ENV=""
if [ "$MAX_MBPS" != 0 ]; then
  OUT="$OUT-max${MAX_MBPS}mbps"
  ENV="RBW_MAX_MBPS=$MAX_MBPS"
fi
go run ./tools/mkwaveform -name radio-bandwidth -version "$VERSION" -arch "$ARCH" \
  -args=--responder -env="$ENV" -out "$OUT.tar.gz" dist/waveform/radio-bandwidth dist/waveform/NOTICES
