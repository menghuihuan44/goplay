/*
 * input_smoke.cpp - minimal manual smoke test for goplay_native.dll input.
 *
 * Default mode is deliberately harmless: it only repositions the pointer to
 * where it already is, sends a zero wheel delta and empty text, and checks
 * argument validation. Pass --unsafe to additionally click once at the current
 * pointer position and press F24 (both are visible side effects).
 */
#include "goplay_native.h"

#include <windows.h>

#include <cstdio>
#include <cstring>

int main(int argc, char** argv) {
    bool unsafe = false;
    for (int i = 1; i < argc; ++i) {
        if (std::strcmp(argv[i], "--unsafe") == 0) {
            unsafe = true;
        } else {
            std::printf("usage: input_smoke [--unsafe]\n");
            return 2;
        }
    }

    std::printf("goplay_native input smoke test (%s)\n",
                unsafe ? "unsafe" : "safe");

    int rc = gp_input_init();
    if (rc != GP_OK) {
        std::printf("FAIL: gp_input_init=%d\n", rc);
        return 1;
    }

    POINT cursor{};
    if (!GetCursorPos(&cursor)) {
        std::printf("FAIL: GetCursorPos=%lu\n", GetLastError());
        return 1;
    }
    std::printf("cursor=(%ld,%ld)\n", cursor.x, cursor.y);

    rc = gp_input_move(static_cast<int>(cursor.x), static_cast<int>(cursor.y));
    std::printf("gp_input_move(same) -> %d\n", rc);
    if (rc != GP_OK) {
        std::printf("last_error=%d\n", gp_input_last_error());
        return 1;
    }

    rc = gp_input_move_rel(0, 0);
    std::printf("gp_input_move_rel(0,0) -> %d\n", rc);
    if (rc != GP_OK) return 1;

    rc = gp_input_wheel(0);
    std::printf("gp_input_wheel(0) -> %d\n", rc);
    if (rc != GP_OK) return 1;

    rc = gp_input_text(L"");
    std::printf("gp_input_text(empty) -> %d\n", rc);
    if (rc != GP_OK) return 1;

    rc = gp_input_text_utf8("");
    std::printf("gp_input_text_utf8(empty) -> %d\n", rc);
    if (rc != GP_OK) return 1;

    // Malformed UTF-8 must be rejected before any event is synthesized.
    rc = gp_input_text_utf8("\xC3\x28");
    std::printf("gp_input_text_utf8(invalid) -> %d\n", rc);
    if (rc != GP_ERR_UTF8) return 1;

    // Invalid arguments must be rejected, never crash.
    rc = gp_input_click(0, 0, 99, 1);
    std::printf("gp_input_click(bad button) -> %d\n", rc);
    if (rc != GP_ERR_INVALID_ARG) return 1;

    rc = gp_input_text(nullptr);
    std::printf("gp_input_text(NULL) -> %d\n", rc);
    if (rc != GP_ERR_INVALID_ARG) return 1;

    rc = gp_input_text_utf8(nullptr);
    std::printf("gp_input_text_utf8(NULL) -> %d\n", rc);
    if (rc != GP_ERR_INVALID_ARG) return 1;

    rc = gp_input_key_down(0);
    std::printf("gp_input_key_down(0) -> %d\n", rc);
    if (rc != GP_ERR_INVALID_ARG) return 1;

    if (unsafe) {
        std::printf("unsafe: clicking left button at the current pointer\n");
        rc = gp_input_click(static_cast<int>(cursor.x),
                            static_cast<int>(cursor.y), GP_MOUSE_LEFT, 1);
        std::printf("gp_input_click -> %d\n", rc);
        if (rc != GP_OK) return 1;

        rc = gp_input_key_press(VK_F24, 10);
        std::printf("gp_input_key_press(VK_F24) -> %d\n", rc);
        if (rc != GP_OK) return 1;
    }

    std::printf("PASS\n");
    return 0;
}
