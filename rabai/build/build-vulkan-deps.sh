#!/usr/bin/env bash
# Prepares what cross-compiling the Vulkan GPU backends needs:
#   $WORK/vk/sysroot  Vulkan + SPIR-V headers and a MinGW import library for
#                     vulkan-1.dll (generated from the Vulkan registry)
#   $WORK/vk/glslc    a recent shader compiler (Ubuntu's is too old for the
#                     fast cooperative-matrix and integer-dot shaders)
# Usage: build/build-vulkan-deps.sh
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
WORK="${WORK:-$HERE/out}"
VK="$WORK/vk"
HEADERS_TAG="${HEADERS_TAG:-vulkan-sdk-1.4.363.0}"
SHADERC_TAG="${SHADERC_TAG:-v2026.4}"
mkdir -p "$VK"

[ -d "$VK/headers/.git" ] || git clone -q --depth 1 --branch "$HEADERS_TAG" https://github.com/KhronosGroup/Vulkan-Headers "$VK/headers"
[ -d "$VK/shaderc/.git" ] || git clone -q --depth 1 --branch "$SHADERC_TAG" https://github.com/google/shaderc "$VK/shaderc"

# shaderc's pinned dependencies (see its DEPS file).
fetch() { # dir url rev
  [ -d "$VK/shaderc/third_party/$1/.git" ] && return
  mkdir -p "$VK/shaderc/third_party/$1" && git -C "$VK/shaderc/third_party/$1" init -q
  git -C "$VK/shaderc/third_party/$1" fetch -q --depth 1 "$2" "$3"
  git -C "$VK/shaderc/third_party/$1" checkout -q FETCH_HEAD
}
rev() { grep "'$1_revision'" "$VK/shaderc/DEPS" | cut -d"'" -f4; }
fetch glslang https://github.com/KhronosGroup/glslang "$(rev glslang)"
fetch spirv-tools https://github.com/KhronosGroup/SPIRV-Tools "$(rev spirv_tools)"
fetch spirv-headers https://github.com/KhronosGroup/SPIRV-Headers "$(rev spirv_headers)"
cmake -S "$VK/shaderc" -B "$VK/shaderc/build" -G Ninja -DCMAKE_BUILD_TYPE=Release -DSHADERC_SKIP_TESTS=ON -DSHADERC_SKIP_EXAMPLES=ON \
  -DSHADERC_SKIP_COPYRIGHT_CHECK=ON -DSPIRV_SKIP_TESTS=ON -DSPIRV_SKIP_EXECUTABLES=ON -DENABLE_GLSLANG_BINARIES=OFF
ninja -C "$VK/shaderc/build" glslc_exe
cp "$VK/shaderc/build/glslc/glslc" "$VK/glslc"

mkdir -p "$VK/sysroot/include" "$VK/sysroot/lib"
cp -r "$VK/headers/include/vulkan" "$VK/headers/include/vk_video" "$VK/sysroot/include/"
cp -r "$VK/shaderc/third_party/spirv-headers/include/spirv" "$VK/sysroot/include/"
python3 "$HERE/vk-implib.py" "$VK/headers/registry/vk.xml" "$VK/vulkan-1.def"
x86_64-w64-mingw32-dlltool -d "$VK/vulkan-1.def" -l "$VK/sysroot/lib/libvulkan-1.a"
echo "vulkan deps ready in $VK"
