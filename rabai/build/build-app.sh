#!/usr/bin/env bash
# Builds dist/RabAI.exe (Windows x64, GUI subsystem) with the engine,
# UI, icon and manifest embedded. Run build-engine.sh first.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
VERSION="${VERSION:-1.0.0}"
cd "$ROOT"

PAYLOAD=internal/engine/payload/engine-win-x64.zip
[ -f "$PAYLOAD" ] || { echo "missing $PAYLOAD: run build/build-engine.sh" >&2; exit 1; }
ENGINE_VERSION="$(unzip -p "$PAYLOAD" VERSION.txt | tr -d '\r\n')"

WINRES="$(go env GOPATH)/bin/go-winres"
[ -x "$WINRES" ] || go install github.com/tc-hib/go-winres@v0.3.3
"$WINRES" make --in winres/winres.json --out rsrc --arch amd64 \
  --product-version "$VERSION" --file-version "$VERSION"

mkdir -p dist
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath \
  -ldflags "-H windowsgui -s -w -X 'main.version=$VERSION' -X 'main.engineVersion=$ENGINE_VERSION'" \
  -o dist/RabAI.exe .
rm -f rsrc_windows_amd64.syso
echo "built dist/RabAI.exe ($(du -h dist/RabAI.exe | cut -f1)), engine: $ENGINE_VERSION"
