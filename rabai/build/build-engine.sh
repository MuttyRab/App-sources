#!/usr/bin/env bash
# Builds the Windows x64 llama.cpp engine (llama-server.exe + DLLs) with
# MinGW-w64 and packs it into internal/engine/payload/engine-win-x64.zip.
#
# Requirements (Ubuntu 24.04): mingw-w64 g++-mingw-w64-x86-64-posix cmake ninja-build zip git
# Usage: build/build-engine.sh [llama.cpp tag]   (default: v1.0.0)
set -euo pipefail

TAG="${1:-v1.0.0}"
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
WORK="${WORK:-$HERE/out}"
SRC="$WORK/llama.cpp"
BUILD="$WORK/win-cpu"
STAGE="$WORK/engine-win"
OUT="$ROOT/internal/engine/payload/engine-win-x64.zip"

mkdir -p "$WORK"
if [ ! -d "$SRC/.git" ]; then
  git clone --depth 1 --branch "$TAG" https://github.com/ggml-org/llama.cpp "$SRC"
fi

# The compat header supplies Windows 11 power-throttling types missing from
# older MinGW headers, so llama.cpp's anti-core-parking code is compiled in.
cmake -S "$SRC" -B "$BUILD" -G Ninja \
  -DCMAKE_TOOLCHAIN_FILE="$HERE/mingw-toolchain.cmake" \
  -DCMAKE_BUILD_TYPE=Release \
  -DCMAKE_C_FLAGS="-include $HERE/mingw-compat.h" \
  -DCMAKE_CXX_FLAGS="-include $HERE/mingw-compat.h" \
  -DBUILD_SHARED_LIBS=ON -DGGML_BACKEND_DL=ON -DGGML_CPU_ALL_VARIANTS=ON \
  -DGGML_NATIVE=OFF -DGGML_OPENMP=OFF \
  -DLLAMA_OPENSSL=OFF -DLLAMA_BUILD_TESTS=OFF -DLLAMA_BUILD_EXAMPLES=OFF \
  -DLLAMA_BUILD_UI=OFF -DLLAMA_USE_PREBUILT_UI=OFF -DLLAMA_BUILD_APP=OFF -DLLAMA_CONNECT=OFF
ninja -C "$BUILD" llama-server

rm -rf "$STAGE" && mkdir -p "$STAGE"
cp "$BUILD"/bin/*.dll "$BUILD"/bin/llama-server.exe "$STAGE/"
# AMX (Xeon server) variant: never present on laptops/desktops, and it needs
# OS support that some environments lack. Leave it out.
rm -f "$STAGE/ggml-cpu-sapphirerapids.dll"
GCCDIR="$(dirname "$(x86_64-w64-mingw32-g++-posix -print-libgcc-file-name)")"
cp "$GCCDIR/libstdc++-6.dll" "$GCCDIR/libgcc_s_seh-1.dll" /usr/x86_64-w64-mingw32/lib/libwinpthread-1.dll "$STAGE/"
x86_64-w64-mingw32-strip --strip-unneeded "$STAGE"/*.dll "$STAGE"/*.exe
cp "$SRC/LICENSE" "$STAGE/LICENSE-llama.cpp.txt"
printf 'llama.cpp %s (%s)\n' "$TAG" "$(git -C "$SRC" rev-parse --short HEAD)" > "$STAGE/VERSION.txt"

mkdir -p "$(dirname "$OUT")"
rm -f "$OUT"
(cd "$STAGE" && zip -q -9 -r "$OUT" .)
echo "engine: $OUT ($(du -h "$OUT" | cut -f1))"
