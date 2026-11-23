/*
 * capture_smoke.cpp - minimal manual smoke test for goplay_native.dll capture.
 *
 * Build: part of the CMake project. Run: capture_smoke [--ppm out.ppm]
 * It captures three frames, validates the BGRA layout, optionally writes the
 * first frame as a PPM (P6) for a quick visual check, and frees every buffer.
 */
#include "goplay_native.h"

#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <string>
#include <vector>

namespace {

void print_last_error(const char* what) {
    std::printf("%s: gp_capture_last_error=%d\n", what, gp_capture_last_error());
}

bool write_ppm(const char* path, const unsigned char* bgra, int width,
               int height, int stride) {
    FILE* file = std::fopen(path, "wb");
    if (!file) return false;
    std::fprintf(file, "P6\n%d %d\n255\n", width, height);
    std::vector<unsigned char> row(static_cast<size_t>(width) * 3u);
    for (int y = 0; y < height; ++y) {
        const unsigned char* src = bgra + static_cast<size_t>(y) *
                                              static_cast<size_t>(stride);
        for (int x = 0; x < width; ++x) {
            row[static_cast<size_t>(x) * 3u + 0] = src[x * 4 + 2];  // R
            row[static_cast<size_t>(x) * 3u + 1] = src[x * 4 + 1];  // G
            row[static_cast<size_t>(x) * 3u + 2] = src[x * 4 + 0];  // B
        }
        if (std::fwrite(row.data(), 1, row.size(), file) != row.size()) {
            std::fclose(file);
            return false;
        }
    }
    return std::fclose(file) == 0;
}

}  // namespace

int main(int argc, char** argv) {
    const char* ppm_path = nullptr;
    for (int i = 1; i < argc; ++i) {
        if (std::strcmp(argv[i], "--ppm") == 0 && i + 1 < argc) {
            ppm_path = argv[++i];
        } else {
            std::printf("usage: capture_smoke [--ppm out.ppm]\n");
            return 2;
        }
    }

    std::printf("goplay_native capture smoke test\n");
    const int monitors = gp_capture_monitor_count();
    std::printf("monitors=%d\n", monitors);
    if (monitors <= 0) {
        std::printf("FAIL: no monitors reported\n");
        return 1;
    }

    int rc = gp_capture_init(0);
    if (rc != GP_OK) {
        std::printf("FAIL: gp_capture_init(0)=%d\n", rc);
        print_last_error("init");
        return 1;
    }
    std::printf("backend=%s (%d)\n", gp_capture_backend_name(),
                gp_capture_backend());

    bool wrote_ppm = false;
    int good_frames = 0;
    for (int i = 0; i < 3; ++i) {
        unsigned char* data = nullptr;
        int width = 0;
        int height = 0;
        int stride = 0;
        rc = gp_capture_frame(&data, &width, &height, &stride);
        if (rc != GP_OK || !data) {
            std::printf("FAIL: frame %d -> %d\n", i, rc);
            print_last_error("frame");
            break;
        }
        // ABI contract: tightly packed, top-down BGRA.
        const bool geometry_ok = width > 0 && height > 0 &&
                                 stride == width * 4;
        const size_t expected = static_cast<size_t>(stride) *
                                static_cast<size_t>(height);
        const bool sane = geometry_ok && expected >= 4u;
        std::printf("frame %d: %dx%d stride=%d bytes=%zu %s\n", i, width,
                    height, stride, expected, sane ? "ok" : "BAD");
        if (!sane) {
            gp_capture_free(data);
            break;
        }
        if (!wrote_ppm && ppm_path) {
            wrote_ppm = write_ppm(ppm_path, data, width, height, stride);
            std::printf("ppm: %s -> %s\n", ppm_path,
                        wrote_ppm ? "written" : "FAILED");
        }
        gp_capture_free(data);
        ++good_frames;
    }

    gp_capture_close();
    std::printf("captured %d/3 frames\n", good_frames);
    if (good_frames == 0) return 1;
    if (ppm_path && !wrote_ppm) return 1;
    std::printf("PASS\n");
    return 0;
}
