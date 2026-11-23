package localmodel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var (
	// ErrChecksumMismatch means the downloaded bytes do not match the pinned
	// SHA256. The partial file is removed before returning.
	ErrChecksumMismatch = errors.New("localmodel: checksum mismatch")
	// ErrInsufficientDiskSpace means the target volume cannot hold the model
	// plus the safety reserve.
	ErrInsufficientDiskSpace = errors.New("localmodel: insufficient disk space")
	// ErrUnsafeModelFile rejects path traversal in catalog file names.
	ErrUnsafeModelFile = errors.New("localmodel: unsafe model file name")
	// ErrUnpinnedModel means a ModelSpec lacks a positive SizeBytes or a
	// 64-character hex SHA256. Model bytes are never accepted without a pin.
	ErrUnpinnedModel = errors.New("localmodel: model spec must pin SizeBytes and SHA256")
)

const (
	defaultMaxRetries     = 3
	defaultRetryBaseDelay = 500 * time.Millisecond
	defaultReserveBytes   = uint64(64 << 20)
	progressInterval      = 100 * time.Millisecond
)

var defaultDownloadClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ResponseHeaderTimeout: 60 * time.Second,
	},
}

// Progress is emitted at most every 100ms during a download and once at the end.
type Progress struct {
	ModelID        string  `json:"model_id"`
	URL            string  `json:"url"`
	Downloaded     int64   `json:"downloaded_bytes"`
	Total          int64   `json:"total_bytes"`
	Percent        float64 `json:"percent"`
	BytesPerSecond float64 `json:"bytes_per_second"`
	Resumed        bool    `json:"resumed"`
	Done           bool    `json:"done"`
	ElapsedMS      int64   `json:"elapsed_ms"`
}

// DownloadResult describes a completed (or cached) model file.
type DownloadResult struct {
	ModelID     string `json:"model_id"`
	Path        string `json:"path"`
	URL         string `json:"url"`
	Bytes       int64  `json:"bytes"`
	SHA256      string `json:"sha256"`
	Resumed     bool   `json:"resumed"`
	ResumedFrom int64  `json:"resumed_from_bytes"`
	Cached      bool   `json:"cached"`
	DurationMS  int64  `json:"duration_ms"`
}

// Downloader fetches GGUF files into ModelsDir. It is safe to reuse one
// Downloader for several models; the progress callback is invoked serially.
type Downloader struct {
	Client         *http.Client
	Endpoint       string
	ModelsDir      string
	Progress       func(Progress)
	MaxRetries     int
	RetryBaseDelay time.Duration
	ReserveBytes   uint64
	// Force re-downloads even when a valid final file already exists.
	Force bool
}

// NewDownloader returns a downloader with retry and endpoint defaults.
func NewDownloader(modelsDir, endpoint string) *Downloader {
	if strings.TrimSpace(endpoint) == "" {
		endpoint = EndpointFromEnv()
	}
	return &Downloader{
		Endpoint:       strings.TrimRight(endpoint, "/"),
		ModelsDir:      modelsDir,
		MaxRetries:     defaultMaxRetries,
		RetryBaseDelay: defaultRetryBaseDelay,
		ReserveBytes:   defaultReserveBytes,
	}
}

// Download fetches one model. Existing valid files are returned as cached.
// Interrupted transfers leave a .part file that a later call resumes with an
// HTTP Range request when the server supports it.
func (d *Downloader) Download(ctx context.Context, spec ModelSpec) (DownloadResult, error) {
	started := time.Now()
	if err := validateModelSpec(spec); err != nil {
		return DownloadResult{}, err
	}
	if d.ModelsDir == "" {
		return DownloadResult{}, errors.New("localmodel: ModelsDir is empty")
	}
	if err := os.MkdirAll(d.ModelsDir, 0o755); err != nil {
		return DownloadResult{}, fmt.Errorf("create models dir: %w", err)
	}
	endpoint := d.Endpoint
	if endpoint == "" {
		endpoint = EndpointFromEnv()
	}
	url := ModelURL(endpoint, spec)
	dest := filepath.Join(d.ModelsDir, spec.File)
	part := dest + ".part"

	if !d.Force {
		if fi, err := os.Stat(dest); err == nil && fi.Mode().IsRegular() {
			ok, verr := verifyModelFile(dest, spec)
			if verr != nil {
				return DownloadResult{}, fmt.Errorf("verify existing %s: %w", dest, verr)
			}
			if ok {
				return DownloadResult{
					ModelID:    spec.ID,
					Path:       dest,
					URL:        url,
					Bytes:      fi.Size(),
					SHA256:     spec.SHA256,
					Cached:     true,
					DurationMS: time.Since(started).Milliseconds(),
				}, nil
			}
			if err := os.Remove(dest); err != nil {
				return DownloadResult{}, fmt.Errorf("remove invalid cached model %s: %w", dest, err)
			}
		}
	}

	if spec.SizeBytes > 0 {
		var offset int64
		if fi, err := os.Stat(part); err == nil {
			offset = fi.Size()
		}
		need := uint64(0)
		if spec.SizeBytes > offset {
			need = uint64(spec.SizeBytes - offset)
		}
		reserve := d.ReserveBytes
		if reserve == 0 {
			reserve = defaultReserveBytes
		}
		if err := EnsureDiskSpace(d.ModelsDir, need, reserve); err != nil {
			return DownloadResult{}, err
		}
	}

	maxAttempts := 1
	if d.MaxRetries > 0 {
		maxAttempts += d.MaxRetries
	}
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			if err := sleepCtx(ctx, d.backoff(attempt)); err != nil {
				return DownloadResult{}, err
			}
		}
		res, err := d.attempt(ctx, spec, endpoint, url, dest)
		if err == nil {
			res.DurationMS = time.Since(started).Milliseconds()
			return res, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return DownloadResult{}, ctx.Err()
		}
		if !isTransient(err) {
			return DownloadResult{}, err
		}
	}
	if lastErr == nil {
		lastErr = errors.New("unknown error")
	}
	return DownloadResult{}, fmt.Errorf("download %s: failed after %d attempt(s): %w", spec.ID, maxAttempts, lastErr)
}

func (d *Downloader) backoff(attempt int) time.Duration {
	base := d.RetryBaseDelay
	if base <= 0 {
		base = defaultRetryBaseDelay
	}
	delay := base << (attempt - 1)
	if delay > 4*time.Second {
		delay = 4 * time.Second
	}
	return delay
}

func (d *Downloader) attempt(ctx context.Context, spec ModelSpec, endpoint, url, dest string) (DownloadResult, error) {
	part := dest + ".part"
	var offset int64
	if fi, err := os.Stat(part); err == nil {
		offset = fi.Size()
	} else if !os.IsNotExist(err) {
		return DownloadResult{}, fmt.Errorf("stat partial file: %w", err)
	}
	if spec.SizeBytes > 0 && offset > spec.SizeBytes {
		if err := os.Remove(part); err != nil && !os.IsNotExist(err) {
			return DownloadResult{}, fmt.Errorf("remove oversized partial file: %w", err)
		}
		offset = 0
	}
	// A complete .part can happen if the process died between the final byte and
	// the rename. Verify it before issuing a request.
	if spec.SizeBytes > 0 && offset == spec.SizeBytes {
		ok, err := verifyModelFile(part, spec)
		if err != nil {
			return DownloadResult{}, err
		}
		if ok {
			if err := os.Rename(part, dest); err != nil {
				return DownloadResult{}, fmt.Errorf("promote verified partial file: %w", err)
			}
			return DownloadResult{ModelID: spec.ID, Path: dest, URL: url, Bytes: offset, SHA256: spec.SHA256, Resumed: true, ResumedFrom: offset}, nil
		}
		if err := os.Remove(part); err != nil && !os.IsNotExist(err) {
			return DownloadResult{}, fmt.Errorf("remove corrupt partial file: %w", err)
		}
		offset = 0
	}

	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return DownloadResult{}, fmt.Errorf("open partial file: %w", err)
	}
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			f.Close()
			return DownloadResult{}, err
		}
	} else {
		if err := f.Truncate(0); err != nil {
			f.Close()
			return DownloadResult{}, err
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			f.Close()
			return DownloadResult{}, err
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		f.Close()
		return DownloadResult{}, err
	}
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("User-Agent", "goplay-localmodel/1")
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}

	started := time.Now()
	resp, err := d.httpClient().Do(req)
	if err != nil {
		f.Close()
		return DownloadResult{}, transientf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	resumedFrom := offset
	var total int64
	switch resp.StatusCode {
	case http.StatusOK:
		if offset > 0 {
			// The server ignored Range; restart the file from byte zero.
			if err := f.Truncate(0); err != nil {
				f.Close()
				return DownloadResult{}, err
			}
			if _, err := f.Seek(0, io.SeekStart); err != nil {
				f.Close()
				return DownloadResult{}, err
			}
			resumedFrom = 0
			offset = 0
		}
		total = resp.ContentLength
		if total < 0 {
			total = spec.SizeBytes
		}
		if spec.SizeBytes > 0 && total > 0 && total != spec.SizeBytes {
			f.Close()
			_ = os.Remove(part)
			return DownloadResult{}, fmt.Errorf("server size %d does not match catalog size %d for %s", total, spec.SizeBytes, spec.ID)
		}
	case http.StatusPartialContent:
		start, complete, ok := parseContentRange(resp.Header.Get("Content-Range"))
		if ok && start != offset {
			f.Close()
			return DownloadResult{}, fmt.Errorf("server resumed at byte %d, want %d", start, offset)
		}
		if ok && complete > 0 {
			total = complete
		} else {
			total = spec.SizeBytes
		}
		if spec.SizeBytes > 0 && total > 0 && total != spec.SizeBytes {
			f.Close()
			_ = os.Remove(part)
			return DownloadResult{}, fmt.Errorf("server size %d does not match catalog size %d for %s", total, spec.SizeBytes, spec.ID)
		}
	case http.StatusRequestedRangeNotSatisfiable:
		f.Close()
		_, complete, _ := parseContentRange(resp.Header.Get("Content-Range"))
		if complete > 0 && complete == offset {
			ok, verr := verifyModelFile(part, spec)
			if verr == nil && ok {
				if err := os.Rename(part, dest); err != nil {
					return DownloadResult{}, fmt.Errorf("promote verified partial file: %w", err)
				}
				return DownloadResult{ModelID: spec.ID, Path: dest, URL: url, Bytes: offset, SHA256: spec.SHA256, Resumed: true, ResumedFrom: offset}, nil
			}
		}
		_ = os.Remove(part)
		return DownloadResult{}, transientf("HTTP 416 for %s; partial file discarded", url)
	default:
		body := readStatusBody(resp.Body)
		f.Close()
		err := fmt.Errorf("GET %s: HTTP %d: %s", url, resp.StatusCode, body)
		if retryableStatus(resp.StatusCode) {
			return DownloadResult{}, &transientError{err: err}
		}
		return DownloadResult{}, err
	}

	pr := &progressReader{
		r:           resp.Body,
		startOffset: offset,
		total:       total,
		started:     started,
		modelID:     spec.ID,
		url:         url,
		resumed:     resumedFrom > 0,
		cb:          d.Progress,
	}
	buf := make([]byte, 128<<10)
	written, copyErr := io.CopyBuffer(f, pr, buf)
	if copyErr != nil {
		f.Close()
		if ctx.Err() != nil {
			return DownloadResult{}, ctx.Err()
		}
		return DownloadResult{}, transientf("read %s after %d bytes: %w", url, offset+written, copyErr)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return DownloadResult{}, transientf("sync partial file: %w", err)
	}
	if err := f.Close(); err != nil {
		return DownloadResult{}, transientf("close partial file: %w", err)
	}
	pr.emit(time.Now(), true)

	finalSize := offset + written
	if spec.SizeBytes > 0 && finalSize > spec.SizeBytes {
		_ = os.Remove(part)
		return DownloadResult{}, fmt.Errorf("downloaded %d bytes, larger than catalog size %d", finalSize, spec.SizeBytes)
	}
	if spec.SizeBytes > 0 && finalSize < spec.SizeBytes {
		return DownloadResult{}, transientf("short download: got %d of %d bytes", finalSize, spec.SizeBytes)
	}
	if spec.SHA256 != "" {
		got, err := FileSHA256(part)
		if err != nil {
			return DownloadResult{}, err
		}
		if !strings.EqualFold(got, spec.SHA256) {
			_ = os.Remove(part)
			return DownloadResult{}, transientf("%w: got %s want %s", ErrChecksumMismatch, got, spec.SHA256)
		}
	}
	if err := os.Rename(part, dest); err != nil {
		return DownloadResult{}, fmt.Errorf("atomic rename %s -> %s: %w", part, dest, err)
	}
	return DownloadResult{
		ModelID:     spec.ID,
		Path:        dest,
		URL:         url,
		Bytes:       finalSize,
		SHA256:      spec.SHA256,
		Resumed:     resumedFrom > 0,
		ResumedFrom: resumedFrom,
	}, nil
}

func (d *Downloader) httpClient() *http.Client {
	if d.Client != nil {
		return d.Client
	}
	return defaultDownloadClient
}

func validateModelSpec(spec ModelSpec) error {
	if strings.TrimSpace(spec.ID) == "" || strings.TrimSpace(spec.Repo) == "" || strings.TrimSpace(spec.File) == "" {
		return errors.New("localmodel: model spec needs ID, Repo and File")
	}
	if spec.File == "." || spec.File == ".." || filepath.Base(spec.File) != spec.File || strings.ContainsAny(spec.File, "/\\") {
		return fmt.Errorf("%w: %q", ErrUnsafeModelFile, spec.File)
	}
	if !validHuggingFaceRepo(spec.Repo) {
		return fmt.Errorf("%w: repo %q", ErrUnsafeModelFile, spec.Repo)
	}
	if spec.SizeBytes <= 0 {
		return fmt.Errorf("%w: %s SizeBytes=%d", ErrUnpinnedModel, spec.ID, spec.SizeBytes)
	}
	if len(spec.SHA256) != 64 {
		return fmt.Errorf("%w: %s SHA256 must be 64 hex characters", ErrUnpinnedModel, spec.ID)
	}
	if _, err := hex.DecodeString(spec.SHA256); err != nil {
		return fmt.Errorf("%w: %s SHA256: %v", ErrUnpinnedModel, spec.ID, err)
	}
	return nil
}

// validHuggingFaceRepo accepts exactly one owner/name path segment and rejects
// separators or query characters that could escape the endpoint's model path.
func validHuggingFaceRepo(repo string) bool {
	if strings.TrimSpace(repo) != repo || repo == "" || strings.ContainsAny(repo, "\\?#%:") {
		return false
	}
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	for _, p := range parts {
		if p == "." || p == ".." || strings.Contains(p, "..") {
			return false
		}
	}
	return true
}

// EnsureDiskSpace checks that dir has need+reserve bytes available.
func EnsureDiskSpace(dir string, need, reserve uint64) error {
	free, err := freeDiskSpace(dir)
	if err != nil {
		return fmt.Errorf("check disk space in %s: %w", dir, err)
	}
	required := need
	if ^uint64(0)-required < reserve {
		required = ^uint64(0)
	} else {
		required += reserve
	}
	if free < required {
		return fmt.Errorf("%w: need %s, free %s in %s", ErrInsufficientDiskSpace,
			HumanBytes(int64(required)), HumanBytes(int64(free)), dir)
	}
	return nil
}

// FileSHA256 returns the lowercase hex SHA256 of a file.
func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func verifyModelFile(path string, spec ModelSpec) (bool, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	if !fi.Mode().IsRegular() {
		return false, fmt.Errorf("%s is not a regular file", path)
	}
	if spec.SizeBytes > 0 && fi.Size() != spec.SizeBytes {
		return false, nil
	}
	if spec.SHA256 == "" {
		return true, nil
	}
	got, err := FileSHA256(path)
	if err != nil {
		return false, err
	}
	return strings.EqualFold(got, spec.SHA256), nil
}

type progressReader struct {
	r           io.Reader
	startOffset int64
	read        int64
	total       int64
	started     time.Time
	last        time.Time
	modelID     string
	url         string
	resumed     bool
	cb          func(Progress)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.read += int64(n)
	if p.cb != nil {
		now := time.Now()
		if p.last.IsZero() || now.Sub(p.last) >= progressInterval {
			p.emit(now, false)
			p.last = now
		}
	}
	return n, err
}

func (p *progressReader) emit(now time.Time, done bool) {
	if p.cb == nil {
		return
	}
	downloaded := p.startOffset + p.read
	percent := 0.0
	if p.total > 0 {
		percent = float64(downloaded) / float64(p.total) * 100
		if percent > 100 {
			percent = 100
		}
	}
	elapsed := now.Sub(p.started)
	speed := 0.0
	if elapsed > 0 {
		speed = float64(p.read) / elapsed.Seconds()
	}
	p.cb(Progress{
		ModelID:        p.modelID,
		URL:            p.url,
		Downloaded:     downloaded,
		Total:          p.total,
		Percent:        percent,
		BytesPerSecond: speed,
		Resumed:        p.resumed,
		Done:           done,
		ElapsedMS:      elapsed.Milliseconds(),
	})
}

func parseContentRange(v string) (start, total int64, ok bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, 0, false
	}
	if !strings.HasPrefix(v, "bytes ") {
		return 0, 0, false
	}
	v = strings.TrimSpace(strings.TrimPrefix(v, "bytes "))
	rangePart, totalPart, found := strings.Cut(v, "/")
	if !found {
		return 0, 0, false
	}
	if totalPart != "*" {
		n, err := strconv.ParseInt(totalPart, 10, 64)
		if err != nil || n < 0 {
			return 0, 0, false
		}
		total = n
	}
	if rangePart == "*" {
		return 0, total, true
	}
	a, b, found := strings.Cut(rangePart, "-")
	if !found {
		return 0, 0, false
	}
	s, err := strconv.ParseInt(a, 10, 64)
	if err != nil || s < 0 {
		return 0, 0, false
	}
	if b != "" {
		if e, err := strconv.ParseInt(b, 10, 64); err != nil || e < s {
			return 0, 0, false
		}
	}
	return s, total, true
}

func readStatusBody(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 4096))
	return strings.TrimSpace(string(b))
}

func retryableStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

type transientError struct{ err error }

func (e *transientError) Error() string { return e.err.Error() }
func (e *transientError) Unwrap() error { return e.err }

func transientf(format string, args ...any) error {
	return &transientError{err: fmt.Errorf(format, args...)}
}

func isTransient(err error) bool {
	var t *transientError
	return errors.As(err, &t)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
