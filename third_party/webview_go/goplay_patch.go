package webview

// goplayPatchRevision is bumped whenever the vendored C++ header is patched.
// Go's cgo cache has been observed to miss header-only edits; a source change
// forces a fresh compile of this package.
const goplayPatchRevision = 1
