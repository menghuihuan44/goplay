cmake_minimum_required(VERSION 3.20)

project(goplay_native LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

if(NOT WIN32)
    message(FATAL_ERROR "goplay_native targets Windows only")
endif()

option(GOPLAY_BUILD_TESTS "Build the native smoke tests" ON)

if(MSVC)
    add_compile_options(/W4 /permissive- /utf-8)
else()
    # Exceptions never cross the C ABI; build the native code without them.
    # The capture/input translation units also avoid std::string/vector and
    # std::thread so MinGW's libstdc++ exception allocator and libwinpthread
    # are not linked into goplay_native.dll.  The overlay exe still uses
    # std::wstring/std::string; see README section 6 for its import-table note.
    add_compile_options(-Wall -Wextra -Wpedantic -fno-exceptions)
    if(MINGW)
        # Keep goplay_native.dll / goplay-overlay.exe free of MSYS2 runtime
        # DLLs (libstdc++-6.dll, libgcc_s_seh-1.dll, libwinpthread-1.dll).
        add_link_options(-static-libgcc -static-libstdc++ -static)
    endif()
endif()

# Common private definitions for the DLL and the overlay executable.
set(GOPLAY_WIN_DEFS UNICODE _UNICODE)

# ---------------------------------------------------------------------------
# goplay_native.dll: capture + input C ABI
# ---------------------------------------------------------------------------
add_library(goplay_native SHARED
    src/capture.cpp
    src/input.cpp
)
target_include_directories(goplay_native
    PUBLIC  ${CMAKE_CURRENT_SOURCE_DIR}/include
    PRIVATE ${CMAKE_CURRENT_SOURCE_DIR}/src
)
target_compile_definitions(goplay_native PRIVATE GP_NATIVE_BUILD ${GOPLAY_WIN_DEFS})
target_link_libraries(goplay_native PRIVATE d3d11 dxgi dxguid uuid gdi32 user32)
set_target_properties(goplay_native PROPERTIES
    OUTPUT_NAME "goplay_native"
    PREFIX ""
    WINDOWS_EXPORT_ALL_SYMBOLS OFF
)

# ---------------------------------------------------------------------------
# goplay-overlay.exe: tray + status window (no WebView2)
# ---------------------------------------------------------------------------
add_executable(goplay-overlay WIN32
    src/main.cpp
    src/overlay.cpp
)
target_include_directories(goplay-overlay PRIVATE
    ${CMAKE_CURRENT_SOURCE_DIR}/include
    ${CMAKE_CURRENT_SOURCE_DIR}/src
)
target_compile_definitions(goplay-overlay PRIVATE ${GOPLAY_WIN_DEFS})
target_link_libraries(goplay-overlay PRIVATE winhttp shell32 gdi32 user32)

# ---------------------------------------------------------------------------
# Smoke tests. They link against the DLL's import library, which also checks
# that the exported C symbols are usable exactly as Go/cgo will use them.
# ---------------------------------------------------------------------------
if(GOPLAY_BUILD_TESTS)
    enable_testing()

    add_executable(capture_smoke tests/capture_smoke.cpp)
    target_include_directories(capture_smoke PRIVATE ${CMAKE_CURRENT_SOURCE_DIR}/include)
    target_link_libraries(capture_smoke PRIVATE goplay_native)

    add_executable(input_smoke tests/input_smoke.cpp)
    target_include_directories(input_smoke PRIVATE ${CMAKE_CURRENT_SOURCE_DIR}/include)
    target_link_libraries(input_smoke PRIVATE goplay_native user32)

    add_test(NAME capture_smoke COMMAND capture_smoke)
    add_test(NAME input_smoke COMMAND input_smoke)
    set_tests_properties(capture_smoke input_smoke PROPERTIES TIMEOUT 30)
endif()

# Convenience copy: keep the DLL next to the overlay exe in multi-target
# generators too, although Ninja/Makefile put both in the same directory.
add_custom_command(TARGET goplay-overlay POST_BUILD
    COMMAND ${CMAKE_COMMAND} -E copy_if_different
            "$<TARGET_FILE:goplay_native>"
            "$<TARGET_FILE_DIR:goplay-overlay>/$<TARGET_FILE_NAME:goplay_native>"
)
