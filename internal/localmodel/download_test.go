package localmodel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testModelSpec(data []byte) ModelSpec {
	sum := sha256.Sum256(data)
	return ModelSpec{
		ID:            "test-model",
		Repo:          "test/model",
		File:          "test-model.gguf",
		Quant:         "Q4_K_M",
		License:       "test-only",
		SizeBytes:     int64(len(data)),
		SHA256:        hex.EncodeToString(sum[:]),
		ContextTokens: 2048,
	}
}

func newTestDownloader(t *testing.T, endpoint string) *Downloader {
	t.Helper()
	d := NewDownloader(t.TempDir(), endpoint)
	d.Client = &http.Client{}
	d.MaxRetries = 0
	d.RetryBaseDelay = time.Millisecond
	d.ReserveBytes = 1 << 20
	return d
}

func TestDownloadFreshAndProgress(t *testing.T) {
	data := []byte(strings.Repeat("gguf-bytes-", 4096))
	spec := testModelSpec(data)
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		if got := r.Header.Get("Range"); got != "" {
			t.Errorf("fresh download sent Range %q", got)
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	d := newTestDownloader(t, srv.URL)
	var last Progress
	d.Progress = func(p Progress) { last = p }
	res, err := d.Download(context.Background(), spec)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if res.Cached || res.Resumed || res.Bytes != int64(len(data)) || res.SHA256 != spec.SHA256 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if atomic.LoadInt32(&requests) != 1 {
		t.Errorf("requests = %d, want 1", requests)
	}
	got, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Errorf("downloaded content mismatch")
	}
	if _, err := os.Stat(res.Path + ".part"); !os.IsNotExist(err) {
		t.Errorf(".part should be gone, stat err=%v", err)
	}
	if !last.Done || last.Downloaded != int64(len(data)) || last.Percent != 100 || last.ModelID != spec.ID {
		t.Errorf("final progress = %+v", last)
	}
	if last.URL != ModelURL(srv.URL, spec) {
		t.Errorf("progress URL = %q", last.URL)
	}
}

func TestDownloadUsesCache(t *testing.T) {
	data := []byte(strings.Repeat("cached-", 1024))
	spec := testModelSpec(data)
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	d := newTestDownloader(t, srv.URL)
	dest := filepath.Join(d.ModelsDir, spec.File)
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := d.Download(context.Background(), spec)
	if err != nil {
		t.Fatalf("Download cached: %v", err)
	}
	if !res.Cached || res.Bytes != int64(len(data)) {
		t.Fatalf("result = %+v", res)
	}
	if atomic.LoadInt32(&requests) != 0 {
		t.Errorf("cached download made %d HTTP requests", requests)
	}
}

func TestDownloadChecksumMismatchRemovesPartial(t *testing.T) {
	data := []byte(strings.Repeat("checksum-", 1000))
	spec := testModelSpec(data)
	spec.SHA256 = strings.Repeat("0", 64)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	d := newTestDownloader(t, srv.URL)
	_, err := d.Download(context.Background(), spec)
	if err == nil {
		t.Fatal("Download with bad checksum succeeded")
	}
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("error = %v, want ErrChecksumMismatch", err)
	}
	dest := filepath.Join(d.ModelsDir, spec.File)
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Errorf("final file should not exist, stat err=%v", statErr)
	}
	if _, statErr := os.Stat(dest + ".part"); !os.IsNotExist(statErr) {
		t.Errorf("partial should be removed, stat err=%v", statErr)
	}
}

func TestDownloadResumesWithRange(t *testing.T) {
	data := []byte(strings.Repeat("resume-", 2000))
	spec := testModelSpec(data)
	const prefix = 300
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := fmt.Sprintf("bytes=%d-", prefix)
		if got := r.Header.Get("Range"); got != want {
			t.Errorf("Range = %q, want %q", got, want)
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", prefix, len(data)-1, len(data)))
		w.Header().Set("Content-Length", strconv.Itoa(len(data)-prefix))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(data[prefix:])
	}))
	defer srv.Close()

	d := newTestDownloader(t, srv.URL)
	dest := filepath.Join(d.ModelsDir, spec.File)
	if err := os.WriteFile(dest+".part", data[:prefix], 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := d.Download(context.Background(), spec)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !res.Resumed || res.ResumedFrom != prefix || res.Bytes != int64(len(data)) {
		t.Fatalf("result = %+v", res)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Errorf("resumed content mismatch")
	}
}

func TestDownloadRestartsWhenServerIgnoresRange(t *testing.T) {
	data := []byte(strings.Repeat("ignore-range-", 1000))
	spec := testModelSpec(data)
	const prefix = 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Range"); got == "" {
			t.Errorf("expected a Range request, got none")
		}
		// Deliberately ignore Range and return the whole file.
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	d := newTestDownloader(t, srv.URL)
	dest := filepath.Join(d.ModelsDir, spec.File)
	if err := os.WriteFile(dest+".part", data[:prefix], 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := d.Download(context.Background(), spec)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if res.Resumed || res.ResumedFrom != 0 || res.Bytes != int64(len(data)) {
		t.Fatalf("result = %+v", res)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Errorf("restarted content mismatch")
	}
}

func TestDownloadRetriesTransientFailure(t *testing.T) {
	data := []byte(strings.Repeat("retry-", 512))
	spec := testModelSpec(data)
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&requests, 1) == 1 {
			http.Error(w, "temporary", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	d := newTestDownloader(t, srv.URL)
	d.MaxRetries = 1
	res, err := d.Download(context.Background(), spec)
	if err != nil {
		t.Fatalf("Download after retry: %v", err)
	}
	if got := atomic.LoadInt32(&requests); got != 2 {
		t.Fatalf("requests = %d, want 2", got)
	}
	if res.Bytes != int64(len(data)) || res.Cached {
		t.Fatalf("result = %+v", res)
	}
}

func TestParseContentRange(t *testing.T) {
	cases := []struct {
		in           string
		start, total int64
		ok           bool
	}{
		{"", 0, 0, false},
		{"bytes 0-99/100", 0, 100, true},
		{"bytes 100-199/200", 100, 200, true},
		{"bytes */123", 0, 123, true},
		{"bytes 0-99/*", 0, 0, true},
		{"items 0-99/100", 0, 0, false},
		{"bytes x-y/100", 0, 0, false},
		{"bytes 9-3/100", 0, 0, false},
	}
	for _, c := range cases {
		start, total, ok := parseContentRange(c.in)
		if start != c.start || total != c.total || ok != c.ok {
			t.Errorf("parseContentRange(%q) = (%d,%d,%v), want (%d,%d,%v)", c.in, start, total, ok, c.start, c.total, c.ok)
		}
	}
}

func TestDownloadRejectsUnpinnedModelSpec(t *testing.T) {
	d := newTestDownloader(t, "http://127.0.0.1:1")
	base := testModelSpec([]byte("x"))
	cases := []struct {
		name string
		edit func(*ModelSpec)
		want error
	}{
		{"missing sha", func(s *ModelSpec) { s.SHA256 = "" }, ErrUnpinnedModel},
		{"short sha", func(s *ModelSpec) { s.SHA256 = "abc" }, ErrUnpinnedModel},
		{"non-hex sha", func(s *ModelSpec) { s.SHA256 = strings.Repeat("z", 64) }, ErrUnpinnedModel},
		{"missing size", func(s *ModelSpec) { s.SizeBytes = 0 }, ErrUnpinnedModel},
		{"repo traversal", func(s *ModelSpec) { s.Repo = "owner/../model" }, ErrUnsafeModelFile},
		{"repo extra segment", func(s *ModelSpec) { s.Repo = "owner/model/extra" }, ErrUnsafeModelFile},
		{"repo query", func(s *ModelSpec) { s.Repo = "owner/model?download=true" }, ErrUnsafeModelFile},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spec := base
			c.edit(&spec)
			if _, err := d.Download(context.Background(), spec); !errors.Is(err, c.want) {
				t.Fatalf("Download error = %v, want %v", err, c.want)
			}
		})
	}
}

func TestDownloadRejectsUnsafeFileAndDiskSpace(t *testing.T) {
	d := newTestDownloader(t, "http://127.0.0.1:1")
	spec := testModelSpec([]byte("x"))
	spec.File = "../escape.gguf"
	if _, err := d.Download(context.Background(), spec); !errors.Is(err, ErrUnsafeModelFile) {
		t.Errorf("unsafe file error = %v", err)
	}
	if err := EnsureDiskSpace(t.TempDir(), 1<<62, 0); !errors.Is(err, ErrInsufficientDiskSpace) {
		t.Errorf("huge need error = %v, want ErrInsufficientDiskSpace", err)
	}
	if err := EnsureDiskSpace(t.TempDir(), 0, 0); err != nil {
		t.Errorf("zero need should pass: %v", err)
	}
}
