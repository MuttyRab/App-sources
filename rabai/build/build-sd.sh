#!/usr/bin/env bash
# Builds the Windows x64 image engine (stable-diffusion.cpp sd-server) with
# MinGW-w64: one executable, CPU backends for every x86 generation (loaded at
# run time, like llama.cpp's) and an optional Vulkan GPU backend.
#
# Output: $WORK/sd-win (sd-server.exe, ggml DLLs, gpu/ggml-vulkan.dll).
# Requirements: as build-engine.sh, plus Vulkan headers + import library
# (VK_SYSROOT) and a recent glslc (GLSLC). See build-vulkan-deps.sh.
# Usage: build/build-sd.sh [stable-diffusion.cpp commit]
set -euo pipefail

REV="${1:-52491bd}"
HERE="$(cd "$(dirname "$0")" && pwd)"
WORK="${WORK:-$HERE/out}"
SRC="$WORK/stable-diffusion.cpp"
BUILD="$WORK/sd-win-build"
STAGE="$WORK/sd-win"
VK_SYSROOT="${VK_SYSROOT:-$WORK/vk/sysroot}"
GLSLC="${GLSLC:-glslc}"

mkdir -p "$WORK"
if [ ! -d "$SRC/.git" ]; then
  git clone --recurse-submodules https://github.com/leejet/stable-diffusion.cpp "$SRC"
  git -C "$SRC" checkout "$REV"
  git -C "$SRC" submodule update --init --recursive
fi

cmake -S "$SRC" -B "$BUILD" -G Ninja \
  -DCMAKE_TOOLCHAIN_FILE="$HERE/mingw-toolchain.cmake" \
  -DCMAKE_BUILD_TYPE=Release \
  -DCMAKE_C_FLAGS="-include $HERE/mingw-compat.h" \
  -DCMAKE_CXX_FLAGS="-include $HERE/mingw-compat.h -Wa,-mbig-obj" \
  -DSD_BUILD_SHARED_GGML_LIB=ON -DBUILD_SHARED_LIBS=ON \
  -DGGML_BACKEND_DL=ON -DGGML_CPU_ALL_VARIANTS=ON -DGGML_NATIVE=OFF -DGGML_OPENMP=OFF \
  -DSD_VULKAN=ON -DVulkan_INCLUDE_DIR="$VK_SYSROOT/include" -DVulkan_LIBRARY="$VK_SYSROOT/lib/libvulkan-1.a" \
  -DVulkan_GLSLC_EXECUTABLE="$GLSLC" \
  -DSD_SERVER_BUILD_FRONTEND=OFF -DSD_BUILD_EXAMPLES=ON
ninja -C "$BUILD" sd-server

rm -rf "$STAGE" && mkdir -p "$STAGE/gpu"
cp "$BUILD"/bin/sd-server.exe "$STAGE/"
for f in "$BUILD"/bin/*.dll; do
  case "$(basename "$f")" in
    ggml-vulkan.dll) cp "$f" "$STAGE/gpu/" ;;
    ggml-cpu-sapphirerapids.dll) ;; # Xeon-only AMX variant
    *) cp "$f" "$STAGE/" ;;
  esac
done
GCCDIR="$(dirname "$(x86_64-w64-mingw32-g++-posix -print-libgcc-file-name)")"
cp "$GCCDIR/libstdc++-6.dll" "$GCCDIR/libgcc_s_seh-1.dll" /usr/x86_64-w64-mingw32/lib/libwinpthread-1.dll "$STAGE/"
x86_64-w64-mingw32-strip --strip-unneeded "$STAGE"/*.dll "$STAGE"/*.exe "$STAGE"/gpu/*.dll
cp "$SRC/LICENSE" "$STAGE/LICENSE-stable-diffusion.cpp.txt"
printf 'stable-diffusion.cpp %s\n' "$(git -C "$SRC" rev-parse --short HEAD)" > "$STAGE/VERSION.txt"
echo "image engine: $STAGE ($(du -sh "$STAGE" | cut -f1))"
