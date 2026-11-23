# llama.cpp Android runtime (GoPlay)

This directory contains the prebuilt **arm64-v8a** `llama-server` runtime that
GoPlay ships inside the Android APK. It is not loaded into the app process: the
Kotlin shell starts it (and the Go core) as separate child processes so local
models can run on the phone without a PC.

- Upstream: https://github.com/ggml-org/llama.cpp
- Upstream commit: `51fc2f9`
- License: MIT (see `LICENSE.llama.cpp`)
- File: `arm64-v8a/libllama-server.so`
- Size: 13,174,768 bytes
- SHA256: `1B83533E981E8E1BBE599C17446E63A6D7C6303825E1ABE55D3E7B05CADCDAC7`

## How it was built

Built from the pinned upstream commit with the Android NDK r26.1
(`26.1.10909125`), CMake, and the NDK's clang for `aarch64-linux-android26`.
Only the `llama-server` target was built; the resulting ELF was stripped with
`llvm-strip` from the same NDK. Final dependencies are only `libm`, `libdl`
and `libc`.

The release tool copies this file to
`android/app/src/main/jniLibs/arm64-v8a/libllama-server.so`; the Go core is
built from source at release time. The APK still contains **no model weights**:
GGUF files are downloaded on first use according to the device tier.

## Updating

1. Check out the desired upstream commit in `third_party/llama.cpp`.
2. Rebuild `llama-server` for `arm64-v8a` with the NDK and strip it.
3. Replace the `.so`, update the size/SHA256 above, and rebuild the APK.
4. Verify the APK installs on a real arm64 device before publishing.
