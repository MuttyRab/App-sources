#!/usr/bin/env bash
# Builds the Windows x64 engines with MinGW-w64 and packs them into
# internal/engine/payload/engine-win-x64.zip:
#   llama-server.exe + DLLs   chat engine, CPU backends for every x86 generation
#   gpu/ggml-vulkan.dll       GPU backend, loaded only when a model uses the GPU
#   sd/                       image engine (build-sd.sh) with tiny decoders in sd/tae
#
# Requirements (Ubuntu 24.04): mingw-w64 g++-mingw-w64-x86-64-posix cmake ninja-build zip git,
# plus the Vulkan toolchain from build-vulkan-deps.sh. Run build-sd.sh first
# to include the image engine.
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
VK="$WORK/vk"
SD_STAGE="${SD_STAGE:-$WORK/sd-win}"

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
  -DGGML_VULKAN=ON -DVulkan_INCLUDE_DIR="$VK/sysroot/include" -DVulkan_LIBRARY="$VK/sysroot/lib/libvulkan-1.a" \
  -DVulkan_GLSLC_EXECUTABLE="$VK/glslc" \
  -DLLAMA_OPENSSL=OFF -DLLAMA_BUILD_TESTS=OFF -DLLAMA_BUILD_EXAMPLES=OFF \
  -DLLAMA_BUILD_UI=OFF -DLLAMA_USE_PREBUILT_UI=OFF -DLLAMA_BUILD_APP=OFF -DLLAMA_CONNECT=OFF
ninja -C "$BUILD" llama-server ggml-vulkan

rm -rf "$STAGE" && mkdir -p "$STAGE/gpu"
cp "$BUILD"/bin/*.dll "$BUILD"/bin/llama-server.exe "$STAGE/"
# The GPU backend lives in gpu/ so runs on the processor never load it (a
# broken graphics driver then can't affect them); RabAI points the engine at
# it with GGML_BACKEND_PATH when a model uses the GPU.
mv "$STAGE/ggml-vulkan.dll" "$STAGE/gpu/"
# AMX (Xeon server) variant: never present on laptops/desktops, and it needs
# OS support that some environments lack. Leave it out.
rm -f "$STAGE/ggml-cpu-sapphirerapids.dll"
GCCDIR="$(dirname "$(x86_64-w64-mingw32-g++-posix -print-libgcc-file-name)")"
cp "$GCCDIR/libstdc++-6.dll" "$GCCDIR/libgcc_s_seh-1.dll" /usr/x86_64-w64-mingw32/lib/libwinpthread-1.dll "$STAGE/"
x86_64-w64-mingw32-strip --strip-unneeded "$STAGE"/*.dll "$STAGE"/*.exe "$STAGE"/gpu/*.dll
cp "$SRC/LICENSE" "$STAGE/LICENSE-llama.cpp.txt"
printf 'llama.cpp %s (%s)\n' "$TAG" "$(git -C "$SRC" rev-parse --short HEAD)" > "$STAGE/VERSION.txt"

# Image engine and the tiny decoders (TAESD, MIT license) used on the processor.
if [ -f "$SD_STAGE/sd-server.exe" ]; then
  rm -rf "$STAGE/sd" && cp -r "$SD_STAGE" "$STAGE/sd"
  mkdir -p "$STAGE/sd/tae"
  for t in taesd taesdxl; do
    [ -f "$WORK/tae/$t.safetensors" ] || { mkdir -p "$WORK/tae"; curl -sfL -o "$WORK/tae/$t.safetensors" "https://huggingface.co/madebyollin/$t/resolve/main/diffusion_pytorch_model.safetensors"; }
    # Half precision: half the download, same pictures.
    python3 "$HERE/safetensors-f16.py" "$WORK/tae/$t.safetensors" "$STAGE/sd/tae/$t.safetensors"
  done
  printf 'TAESD and TAESDXL by Ollin Boer Bohan, MIT license: https://github.com/madebyollin/taesd\n' > "$STAGE/sd/tae/LICENSE.txt"
else
  echo "note: no image engine at $SD_STAGE (run build-sd.sh); building without picture support" >&2
fi

mkdir -p "$(dirname "$OUT")"
rm -f "$OUT"
(cd "$STAGE" && zip -q -9 -r "$OUT" .)
echo "engine: $OUT ($(du -h "$OUT" | cut -f1))"
