/*
 * overlay.h - internal interface between main.cpp and overlay.cpp.
 * Not part of the public C ABI.
 */
#ifndef GOPLAY_OVERLAY_INTERNAL_H
#define GOPLAY_OVERLAY_INTERNAL_H

#include <windows.h>

#include <string>

struct OverlayOptions {
    std::wstring base_url = L"http://127.0.0.1:8420";
    std::wstring instance_id;
    bool self_test = false;
    int self_test_ms = 1200;
};

/* Runs the overlay message loop; returns a process exit code. */
int gp_overlay_run(HINSTANCE instance, const OverlayOptions& options);

#endif /* GOPLAY_OVERLAY_INTERNAL_H */
