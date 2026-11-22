package youtube

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/seiortech/mtools-sdk/pluginapi"
)

var (
	ErrInvalidURL          = errors.New("invalid URL: must be a valid http(s):// link")
	ErrMissingBinary       = errors.New("yt-dlp binary not found: configure path or download binary")
	ErrMissingFFmpeg       = errors.New("ffmpeg binary not found: required for merging, clipping, audio conversion, or embedding")
	ErrInvalidTimeRange    = errors.New("invalid time range: use HH:MM:SS, MM:SS, or seconds, with start < end")
	ErrInvalidProxyURL     = errors.New("invalid proxy URL: must use http, https, or socks5 scheme")
	ErrInvalidCookiesFile  = errors.New("cookies file not found or is a directory")
	ErrInvalidRateLimit    = errors.New("invalid rate limit: use a number with optional K, M, or G suffix (e.g. 5M)")
	ErrPlaylistFetchFailed = errors.New("failed to fetch playlist metadata")
)

var (
	lookPath   = exec.LookPath
	httpClient = &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   15 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			IdleConnTimeout:       60 * time.Second,
		},
	}
)

// ParseURL validates that raw is a valid HTTP(S) URL with a non-empty host.
func ParseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "-") {
		return "", ErrInvalidURL
	}

	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", ErrInvalidURL
	}

	return raw, nil
}

// ParseProgressLine extracts progress metrics from a yt-dlp stdout line without regexp overhead.
func ParseProgressLine(line string) (pluginapi.Progress, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return pluginapi.Progress{}, false
	}

	// Custom template format: "<pct>%|<speed>|<eta>"
	if pctIdx := strings.IndexByte(line, '%'); pctIdx > 0 {
		rest := line[pctIdx+1:]
		if p1 := strings.IndexByte(rest, '|'); p1 >= 0 {
			afterP1 := rest[p1+1:]
			if p2 := strings.IndexByte(afterP1, '|'); p2 >= 0 {
				if pct, err := strconv.ParseFloat(strings.TrimSpace(line[:pctIdx]), 64); err == nil {
					speed := strings.TrimSpace(afterP1[:p2])
					eta := strings.TrimSpace(afterP1[p2+1:])
					if sp := strings.IndexAny(eta, " \t"); sp >= 0 {
						eta = eta[:sp]
					}
					msg := speed
					if eta != "" {
						if msg != "" {
							msg += " · ETA " + eta
						} else {
							msg = "ETA " + eta
						}
					}
					return pluginapi.Progress{
						Done:    pct,
						Total:   100,
						Unit:    "%",
						Message: msg,
					}, true
				}
			}
		}
	}

	// Fallback yt-dlp format: "[download]  75.0% of ..."
	if after, ok := strings.CutPrefix(line, "[download]"); ok {
		after = strings.TrimSpace(after)
		if pctIdx := strings.IndexByte(after, '%'); pctIdx > 0 {
			if pct, err := strconv.ParseFloat(strings.TrimSpace(after[:pctIdx]), 64); err == nil {
				return pluginapi.Progress{
					Done:    pct,
					Total:   100,
					Unit:    "%",
					Message: line,
				}, true
			}
		}
	}

	return pluginapi.Progress{}, false
}

// BinaryName returns platform-appropriate yt-dlp executable name.
func BinaryName() string {
	if runtime.GOOS == "windows" {
		return "yt-dlp.exe"
	}
	return "yt-dlp"
}

// FFmpegBinaryName returns platform-appropriate ffmpeg executable name.
func FFmpegBinaryName() string {
	if runtime.GOOS == "windows" {
		return "ffmpeg.exe"
	}
	return "ffmpeg"
}

func resolveExecutable(binName, lookName, customPath, pluginDataDir string, notFoundErr error) (string, error) {
	if customPath != "" {
		if fi, err := os.Stat(customPath); err == nil && !fi.IsDir() {
			return customPath, nil
		}
	}

	if pluginDataDir != "" {
		managedRoot := filepath.Join(pluginDataDir, binName)
		if fi, err := os.Stat(managedRoot); err == nil && !fi.IsDir() {
			return managedRoot, nil
		}
		managedBin := filepath.Join(pluginDataDir, "bin", binName)
		if fi, err := os.Stat(managedBin); err == nil && !fi.IsDir() {
			return managedBin, nil
		}
	}

	if path, err := lookPath(lookName); err == nil {
		return path, nil
	}

	return "", notFoundErr
}

// ResolveFFmpeg locates ffmpeg executable in priority order:
// 1. Explicit path
// 2. Plugin data directory (root or bin/ subdir)
// 3. System PATH
func ResolveFFmpeg(customPath, pluginDataDir string) (string, error) {
	return resolveExecutable(FFmpegBinaryName(), "ffmpeg", customPath, pluginDataDir, ErrMissingFFmpeg)
}

// ResolveBinary locates yt-dlp executable in priority order:
// 1. Explicit path
// 2. Plugin data directory (root or bin/ subdir)
// 3. System PATH
func ResolveBinary(customPath, pluginDataDir string) (string, error) {
	return resolveExecutable(BinaryName(), "yt-dlp", customPath, pluginDataDir, ErrMissingBinary)
}

// ReleaseAssetURL returns official standalone GitHub release download URL for current OS and arch.
func ReleaseAssetURL() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		return "https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp_macos", nil
	case "linux":
		if runtime.GOARCH == "arm64" {
			return "https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp_linux_aarch64", nil
		}
		return "https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp_linux", nil
	case "windows":
		return "https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp.exe", nil
	default:
		return "", fmt.Errorf("unsupported OS %q for auto-download", runtime.GOOS)
	}
}

// DownloadBinaryTo downloads the official yt-dlp standalone executable into targetPath.
func DownloadBinaryTo(ctx context.Context, targetPath string, progress func(pluginapi.Progress)) (string, error) {
	assetURL, err := ReleaseAssetURL()
	if err != nil {
		return "", err
	}

	binDir := filepath.Dir(targetPath)
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create destination directory: %w", err)
	}

	tempPath := targetPath + ".tmp"
	if err := downloadURLToTempFile(ctx, assetURL, tempPath, 0755, progress); err != nil {
		return "", err
	}

	if err := os.Chmod(tempPath, 0755); err != nil {
		_ = os.Remove(tempPath)
		return "", fmt.Errorf("failed to make binary executable: %w", err)
	}

	if err := os.Rename(tempPath, targetPath); err != nil {
		_ = os.Remove(tempPath)
		return "", fmt.Errorf("failed to replace binary: %w", err)
	}

	return targetPath, nil
}

const maxFFmpegArchiveBytes = 256 << 20 // 256 MiB safety cap

// FFmpegReleaseAssetURL returns the standalone ffmpeg .zip release URL for the host OS/arch.
func FFmpegReleaseAssetURL() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		return "https://github.com/ffbinaries/ffbinaries-prebuilt/releases/download/v6.1/ffmpeg-6.1-macos-64.zip", nil
	case "linux":
		switch runtime.GOARCH {
		case "arm64":
			return "https://github.com/ffbinaries/ffbinaries-prebuilt/releases/download/v6.1/ffmpeg-6.1-linux-arm-64.zip", nil
		case "amd64":
			return "https://github.com/ffbinaries/ffbinaries-prebuilt/releases/download/v6.1/ffmpeg-6.1-linux-64.zip", nil
		default:
			return "", fmt.Errorf("unsupported Linux architecture %q for ffmpeg auto-download", runtime.GOARCH)
		}
	case "windows":
		return "https://github.com/ffbinaries/ffbinaries-prebuilt/releases/download/v6.1/ffmpeg-6.1-win-64.zip", nil
	default:
		return "", fmt.Errorf("unsupported OS %q for ffmpeg auto-download", runtime.GOOS)
	}
}

// DownloadFFmpegTo downloads the standalone ffmpeg binary for the current OS/arch into targetPath.
func DownloadFFmpegTo(ctx context.Context, targetPath string, progress func(pluginapi.Progress)) (string, error) {
	assetURL, err := FFmpegReleaseAssetURL()
	if err != nil {
		return "", err
	}
	return DownloadFFmpegFromURLTo(ctx, assetURL, targetPath, progress)
}

// DownloadFFmpegFromURLTo downloads a .zip archive from assetURL and atomically installs the ffmpeg binary to targetPath.
func DownloadFFmpegFromURLTo(ctx context.Context, assetURL, targetPath string, progress func(pluginapi.Progress)) (string, error) {
	return downloadFFmpegFromURLWithClient(ctx, httpClient, assetURL, targetPath, progress)
}

func downloadFFmpegFromURLWithClient(ctx context.Context, client *http.Client, assetURL, targetPath string, progress func(pluginapi.Progress)) (string, error) {
	binDir := filepath.Dir(targetPath)
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create destination directory: %w", err)
	}

	tempZip := targetPath + ".zip.tmp"
	defer func() { _ = os.Remove(tempZip) }()

	if err := downloadURLToTempFileWithClient(ctx, client, assetURL, tempZip, 0600, func(pr pluginapi.Progress) {
		if progress != nil {
			// Scale download phase to 0..85% so extraction has room up to 100%.
			pr.Done = pr.Done * 0.85
			progress(pr)
		}
	}); err != nil {
		return "", err
	}

	if progress != nil {
		progress(pluginapi.Progress{
			Done:    90,
			Total:   100,
			Unit:    "%",
			Message: "Extracting ffmpeg binary...",
		})
	}

	if err := extractFFmpegFromZip(ctx, tempZip, targetPath); err != nil {
		return "", err
	}

	return targetPath, nil
}

func extractFFmpegFromZip(ctx context.Context, zipPath, targetPath string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("failed to open ffmpeg archive: %w", err)
	}
	defer zr.Close()

	wantName := FFmpegBinaryName()
	var matched *zip.File
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if strings.Contains(f.Name, "..") {
			return fmt.Errorf("invalid archive entry path %q", f.Name)
		}
		if filepath.Base(f.Name) == wantName {
			matched = f
			break
		}
	}
	if matched == nil {
		return fmt.Errorf("ffmpeg archive missing %q executable", wantName)
	}
	if matched.UncompressedSize64 > maxFFmpegArchiveBytes {
		return fmt.Errorf("ffmpeg binary exceeds %d MiB limit", maxFFmpegArchiveBytes>>20)
	}

	rc, err := matched.Open()
	if err != nil {
		return fmt.Errorf("failed to read ffmpeg from archive: %w", err)
	}
	defer rc.Close()

	tempBin := targetPath + ".tmp"
	out, err := os.OpenFile(tempBin, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		return fmt.Errorf("failed to create temp binary: %w", err)
	}

	limited := io.LimitReader(rc, maxFFmpegArchiveBytes+1)
	buf := make([]byte, 32*1024)
	var written int64
	for {
		select {
		case <-ctx.Done():
			out.Close()
			_ = os.Remove(tempBin)
			return ctx.Err()
		default:
		}
		n, rErr := limited.Read(buf)
		if n > 0 {
			written += int64(n)
			if written > maxFFmpegArchiveBytes {
				out.Close()
				_ = os.Remove(tempBin)
				return fmt.Errorf("ffmpeg binary exceeds %d MiB limit", maxFFmpegArchiveBytes>>20)
			}
			if _, wErr := out.Write(buf[:n]); wErr != nil {
				out.Close()
				_ = os.Remove(tempBin)
				return fmt.Errorf("failed to write extracted binary: %w", wErr)
			}
		}
		if rErr != nil {
			if errors.Is(rErr, io.EOF) {
				break
			}
			out.Close()
			_ = os.Remove(tempBin)
			return fmt.Errorf("failed to extract ffmpeg binary: %w", rErr)
		}
	}

	if err := out.Sync(); err != nil {
		out.Close()
		_ = os.Remove(tempBin)
		return fmt.Errorf("failed to sync extracted binary: %w", err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tempBin)
		return fmt.Errorf("failed to close extracted binary: %w", err)
	}
	if err := os.Chmod(tempBin, 0755); err != nil {
		_ = os.Remove(tempBin)
		return fmt.Errorf("failed to make extracted binary executable: %w", err)
	}
	if err := os.Rename(tempBin, targetPath); err != nil {
		_ = os.Remove(tempBin)
		return fmt.Errorf("failed to install ffmpeg binary: %w", err)
	}
	return nil
}

func downloadURLToTempFile(ctx context.Context, assetURL, tempPath string, mode os.FileMode, progress func(pluginapi.Progress)) error {
	return downloadURLToTempFileWithClient(ctx, httpClient, assetURL, tempPath, mode, progress)
}

func downloadURLToTempFileWithClient(ctx context.Context, client *http.Client, assetURL, tempPath string, mode os.FileMode, progress func(pluginapi.Progress)) error {
	const maxAttempts = 3
	var lastErr error

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if ctx.Err() != nil {
			_ = os.Remove(tempPath)
			return ctx.Err()
		}

		var offset int64
		if fi, err := os.Stat(tempPath); err == nil && fi.Size() > 0 {
			offset = fi.Size()
		}

		err := downloadURLAttempt(ctx, client, assetURL, tempPath, mode, offset, progress)
		if err == nil {
			return nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			_ = os.Remove(tempPath)
			return err
		}
		lastErr = err

		if attempt+1 < maxAttempts {
			delay := time.Duration(1<<attempt) * time.Second
			if progress != nil {
				progress(pluginapi.Progress{
					Done:    0,
					Total:   100,
					Unit:    "%",
					Message: fmt.Sprintf("Download interrupted — resuming in %s (attempt %d/%d)...", delay, attempt+2, maxAttempts),
				})
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				_ = os.Remove(tempPath)
				return ctx.Err()
			case <-timer.C:
			}
		}
	}

	_ = os.Remove(tempPath)
	return lastErr
}

func downloadURLAttempt(ctx context.Context, client *http.Client, assetURL, tempPath string, mode os.FileMode, offset int64, progress func(pluginapi.Progress)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create download request: %w", err)
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download request failed: %w", err)
	}
	defer resp.Body.Close()

	flags := os.O_WRONLY | os.O_CREATE
	var total int64
	var downloaded int64

	switch resp.StatusCode {
	case http.StatusPartialContent:
		flags |= os.O_APPEND
		downloaded = offset
		if resp.ContentLength > 0 {
			total = offset + resp.ContentLength
		}
	case http.StatusOK:
		flags |= os.O_TRUNC
		downloaded = 0
		total = resp.ContentLength
	case http.StatusRequestedRangeNotSatisfiable:
		// File already complete or range invalid; restart cleanly from 0.
		_ = os.Remove(tempPath)
		return fmt.Errorf("download server returned HTTP %d", resp.StatusCode)
	default:
		return fmt.Errorf("download server returned HTTP %d", resp.StatusCode)
	}

	f, err := os.OpenFile(tempPath, flags, mode)
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}

	buf := make([]byte, 128*1024)
	var lastEmit time.Time

	for {
		select {
		case <-ctx.Done():
			f.Close()
			return ctx.Err()
		default:
		}

		n, rErr := resp.Body.Read(buf)
		if n > 0 {
			if _, wErr := f.Write(buf[:n]); wErr != nil {
				f.Close()
				return fmt.Errorf("failed to write download: %w", wErr)
			}
			downloaded += int64(n)
			if progress != nil && total > 0 && (time.Since(lastEmit) >= 100*time.Millisecond || downloaded >= total) {
				lastEmit = time.Now()
				progress(pluginapi.Progress{
					Done:    float64(downloaded) / float64(total) * 100,
					Total:   100,
					Unit:    "%",
					Message: fmt.Sprintf("%.1f / %.1f MB", float64(downloaded)/1024/1024, float64(total)/1024/1024),
				})
			}
		}

		if rErr != nil {
			if errors.Is(rErr, io.EOF) {
				break
			}
			f.Close()
			return fmt.Errorf("error reading response stream: %w", rErr)
		}
	}

	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("failed to sync download: %w", err)
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("failed to close download: %w", err)
	}

	return nil
}

// VideoMetadata stores video attributes returned by yt-dlp.
type VideoMetadata struct {
	ID             string         `json:"id"`
	Title          string         `json:"title"`
	Channel        string         `json:"channel"`
	Uploader       string         `json:"uploader"`
	Duration       float64        `json:"duration"`
	DurationString string         `json:"duration_string"`
	ViewCount      int64          `json:"view_count"`
	ThumbnailURL   string         `json:"thumbnail"`
	Formats        []StreamFormat `json:"formats"`
}

// StreamFormat describes an individual audio or video stream from yt-dlp.
type StreamFormat struct {
	FormatID       string  `json:"format_id"`
	Ext            string  `json:"ext"`
	Resolution     string  `json:"resolution"`
	Height         int     `json:"height"`
	FPS            float64 `json:"fps"`
	VCodec         string  `json:"vcodec"`
	ACodec         string  `json:"acodec"`
	ABR            float64 `json:"abr"`
	Filesize       int64   `json:"filesize"`
	FilesizeApprox int64   `json:"filesize_approx"`
	FormatNote     string  `json:"format_note"`
}

// FormatOption represents a dropdown item with display label and raw format identifier.
type FormatOption struct {
	Label string
	Value string
}

// CommandOptions specifies all parameters to build a yt-dlp execution command.
type CommandOptions struct {
	BinaryPath          string
	FFmpegPath          string
	URL                 string
	OutputDir           string
	VideoStream         string // "bestvideo", "137", "none", etc.
	AudioStream         string // "bestaudio", "140", "none", etc.
	OutputFormat        string // "mp4", "mkv", "webm", "mp3", "m4a", "opus", "flac", "wav"
	AudioQuality        string // "", "0".."9" or "320K", "192K", "128K"
	EmbedSubs           bool
	SubLangs            string
	WriteAutoSubs       bool
	EmbedThumbnail      bool
	EmbedChapters       bool
	EmbedMetadata       bool
	SponsorBlockMode    string   // "", "remove", "mark"
	SponsorBlockCats    []string // sponsor, intro, outro, selfpromo, interaction
	TimeRangeStart      string
	TimeRangeEnd        string
	ForceKeyframesCuts  bool
	CookiesFromBrowser  string // "", "chrome", "firefox", "safari", "edge", "brave"
	CookiesFile         string
	ProxyURL            string
	RateLimit           string
	ConcurrentFragments int
	CustomFilename      string
	IsPlaylistItem      bool
	RedactSecrets       bool
}

// ParseTimestampSeconds parses "HH:MM:SS", "MM:SS", or seconds into total seconds.
func ParseTimestampSeconds(raw string) (float64, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, ErrInvalidTimeRange
	}
	parts := strings.Split(s, ":")
	if len(parts) > 3 {
		return 0, ErrInvalidTimeRange
	}
	var total float64
	for _, p := range parts {
		if p == "" {
			return 0, ErrInvalidTimeRange
		}
		v, err := strconv.ParseFloat(p, 64)
		if err != nil || v < 0 {
			return 0, ErrInvalidTimeRange
		}
		total = total*60 + v
	}
	return total, nil
}

// ParseTimeRange validates start/end timestamps and formats a yt-dlp --download-sections spec.
func ParseTimeRange(start, end string) (string, error) {
	s := strings.TrimSpace(start)
	e := strings.TrimSpace(end)
	if s == "" && e == "" {
		return "", nil
	}
	var startSec, endSec float64
	var err error
	if s != "" {
		startSec, err = ParseTimestampSeconds(s)
		if err != nil {
			return "", ErrInvalidTimeRange
		}
	}
	if e != "" {
		endSec, err = ParseTimestampSeconds(e)
		if err != nil {
			return "", ErrInvalidTimeRange
		}
	}
	if s != "" && e != "" && startSec >= endSec {
		return "", ErrInvalidTimeRange
	}
	if s == "" {
		s = "0"
	}
	if e == "" {
		e = "inf"
	}
	return fmt.Sprintf("*%s-%s", s, e), nil
}

// ParseRateLimit validates a yt-dlp --limit-rate value (e.g. "5M", "500K").
func ParseRateLimit(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", nil
	}
	check := s
	switch check[len(check)-1] {
	case 'K', 'M', 'G', 'k', 'm', 'g':
		check = check[:len(check)-1]
	}
	if check == "" {
		return "", ErrInvalidRateLimit
	}
	v, err := strconv.ParseFloat(check, 64)
	if err != nil || v <= 0 {
		return "", ErrInvalidRateLimit
	}
	return s, nil
}

// ParseProxyURL validates a proxy URL (http, https, socks5, socks5h).
func ParseProxyURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", nil
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", ErrInvalidProxyURL
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
		return s, nil
	default:
		return "", ErrInvalidProxyURL
	}
}

// ValidateCookiesFile checks that a cookies file exists and is not a directory.
func ValidateCookiesFile(path string) (string, error) {
	s := strings.TrimSpace(path)
	if s == "" {
		return "", nil
	}
	fi, err := os.Stat(s)
	if err != nil || fi.IsDir() {
		return "", ErrInvalidCookiesFile
	}
	return s, nil
}

// IsPlaylistURL reports whether rawURL looks like a YouTube playlist or channel URL.
func IsPlaylistURL(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	if u.Query().Get("list") != "" {
		return true
	}
	p := strings.ToLower(u.Path)
	return strings.HasPrefix(p, "/playlist") ||
		strings.HasPrefix(p, "/channel/") ||
		strings.HasPrefix(p, "/@") ||
		strings.HasPrefix(p, "/c/")
}

// NeedsFFmpeg reports whether the selected options require ffmpeg for post-processing or merging.
func NeedsFFmpeg(opts CommandOptions) bool {
	if strings.TrimSpace(opts.TimeRangeStart) != "" || strings.TrimSpace(opts.TimeRangeEnd) != "" {
		return true
	}
	if opts.EmbedSubs || opts.EmbedThumbnail || opts.EmbedChapters || opts.EmbedMetadata {
		return true
	}
	if opts.SponsorBlockMode == "remove" {
		return true
	}
	switch opts.OutputFormat {
	case "mp3", "m4a", "opus", "flac", "wav":
		return true
	}
	video := opts.VideoStream
	if video == "" {
		video = "bestvideo"
	}
	audio := opts.AudioStream
	if audio == "" {
		audio = "bestaudio"
	}
	return video != "none" && audio != "none"
}

func isAudioOnlyFormat(ext string) bool {
	switch ext {
	case "mp3", "m4a", "opus", "flac", "wav":
		return true
	default:
		return false
	}
}

func redactProxyURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = url.User("[REDACTED]")
	return u.String()
}

// BuildArgs constructs the argv slice for yt-dlp execution.
func BuildArgs(opts CommandOptions) []string {
	args := []string{
		"--newline",
		"--progress-template", "%(progress._percent_str)s|%(progress._speed_str)s|%(progress._eta_str)s",
		"--no-playlist",
		"--continue",
		"--retries", "10",
		"--fragment-retries", "10",
		"--retry-sleep", "exp=1:30",
		"--file-access-retries", "3",
		"--socket-timeout", "30",
	}

	if opts.FFmpegPath != "" {
		args = append(args, "--ffmpeg-location", filepath.Dir(opts.FFmpegPath))
	}

	if opts.OutputDir != "" {
		args = append(args, "-P", opts.OutputDir)
	}

	if strings.TrimSpace(opts.CustomFilename) != "" {
		name := filepath.Base(strings.TrimSpace(opts.CustomFilename))
		name = strings.ReplaceAll(name, "/", "")
		name = strings.ReplaceAll(name, "\\", "")
		if name != "" && name != "." {
			if ext := filepath.Ext(name); ext != "" && !strings.Contains(name, "%(") {
				name = strings.TrimSuffix(name, ext) + ".%(ext)s"
			} else if !strings.Contains(name, "%(ext)s") {
				name = name + ".%(ext)s"
			}
			args = append(args, "-o", name)
		}
	}

	video := opts.VideoStream
	if video == "" {
		video = "bestvideo"
	}
	audio := opts.AudioStream
	if audio == "" {
		audio = "bestaudio"
	}

	switch {
	case isAudioOnlyFormat(opts.OutputFormat):
		// Always extract audio without downloading video when an audio-only container is selected.
		args = append(args, "-x", "--audio-format", opts.OutputFormat)
		if opts.AudioQuality != "" {
			args = append(args, "--audio-quality", opts.AudioQuality)
		}
		if audio != "none" && audio != "bestaudio" {
			args = append(args, "-f", audio)
		}
	case video == "none" && audio != "none":
		if audio == "bestaudio" {
			args = append(args, "-x")
		} else {
			args = append(args, "-f", audio)
		}
		if opts.AudioQuality != "" {
			args = append(args, "--audio-quality", opts.AudioQuality)
		}
	case video != "none" && audio == "none":
		args = append(args, "-f", video)
		if opts.OutputFormat != "" {
			args = append(args, "--remux-video", opts.OutputFormat)
		}
	default:
		formatSpec := video + "+" + audio
		if video == "bestvideo" && audio == "bestaudio" {
			formatSpec = "bestvideo+bestaudio/best"
		}
		args = append(args, "-f", formatSpec)
		if opts.OutputFormat != "" {
			args = append(args, "--merge-output-format", opts.OutputFormat)
		}
	}

	if opts.EmbedSubs {
		args = append(args, "--embed-subs")
		if strings.TrimSpace(opts.SubLangs) != "" {
			args = append(args, "--sub-langs", strings.TrimSpace(opts.SubLangs))
		}
		if opts.WriteAutoSubs {
			args = append(args, "--write-auto-subs")
		}
	}

	if opts.EmbedThumbnail {
		args = append(args, "--embed-thumbnail")
	}
	if opts.EmbedChapters {
		args = append(args, "--embed-chapters")
	}
	if opts.EmbedMetadata {
		args = append(args, "--embed-metadata")
	}

	if opts.SponsorBlockMode != "" && len(opts.SponsorBlockCats) > 0 {
		cats := strings.Join(opts.SponsorBlockCats, ",")
		switch opts.SponsorBlockMode {
		case "remove":
			args = append(args, "--sponsorblock-remove", cats)
		case "mark":
			args = append(args, "--sponsorblock-mark", cats)
		}
	}

	if section, err := ParseTimeRange(opts.TimeRangeStart, opts.TimeRangeEnd); err == nil && section != "" {
		args = append(args, "--download-sections", section)
		if opts.ForceKeyframesCuts {
			args = append(args, "--force-keyframes-at-cuts")
		}
	}

	if opts.CookiesFromBrowser != "" && opts.CookiesFromBrowser != "none" {
		args = append(args, "--cookies-from-browser", opts.CookiesFromBrowser)
	} else if strings.TrimSpace(opts.CookiesFile) != "" {
		val := strings.TrimSpace(opts.CookiesFile)
		if opts.RedactSecrets {
			val = "[REDACTED]"
		}
		args = append(args, "--cookies", val)
	}

	if strings.TrimSpace(opts.ProxyURL) != "" {
		val := strings.TrimSpace(opts.ProxyURL)
		if opts.RedactSecrets {
			val = redactProxyURL(val)
		}
		args = append(args, "--proxy", val)
	}

	if strings.TrimSpace(opts.RateLimit) != "" {
		args = append(args, "--limit-rate", strings.TrimSpace(opts.RateLimit))
	}

	if opts.ConcurrentFragments > 1 {
		args = append(args, "-N", strconv.Itoa(opts.ConcurrentFragments))
	}

	if opts.URL != "" {
		args = append(args, "--", opts.URL)
	}

	return args
}

// BuildCommandString formats the exact yt-dlp command line for user display and copy (with secrets redacted).
func BuildCommandString(opts CommandOptions) string {
	opts.RedactSecrets = true
	bin := opts.BinaryPath
	if bin == "" {
		bin = "yt-dlp"
	}
	args := BuildArgs(opts)
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, quoteArg(bin))
	skipNext := false
	for _, arg := range args {
		if skipNext {
			skipNext = false
			continue
		}
		switch arg {
		case "--newline", "--continue":
			continue
		case "--progress-template", "--retries", "--fragment-retries", "--retry-sleep", "--file-access-retries", "--socket-timeout":
			skipNext = true
			continue
		case "--":
			continue
		}
		parts = append(parts, quoteArg(arg))
	}
	return strings.Join(parts, " ")
}

func quoteArg(s string) string {
	if strings.ContainsAny(s, " \t\n\"'$&;|<>*?[]()") {
		return strconv.Quote(s)
	}
	return s
}

const maxStderrCapture = 64 * 1024

// MetadataOptions carries optional auth/proxy settings for metadata preview commands.
type MetadataOptions struct {
	CookiesFromBrowser string
	CookiesFile        string
	ProxyURL           string
}

func appendMetadataOptions(args []string, opts MetadataOptions) []string {
	if opts.CookiesFromBrowser != "" && opts.CookiesFromBrowser != "none" {
		args = append(args, "--cookies-from-browser", opts.CookiesFromBrowser)
	} else if strings.TrimSpace(opts.CookiesFile) != "" {
		args = append(args, "--cookies", strings.TrimSpace(opts.CookiesFile))
	}
	if strings.TrimSpace(opts.ProxyURL) != "" {
		args = append(args, "--proxy", strings.TrimSpace(opts.ProxyURL))
	}
	return args
}

func runMetadataCommand(ctx context.Context, binPath string, args []string, onLog func(string)) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binPath, args...)
	prepareCommand(cmd)
	cmd.WaitDelay = 2 * time.Second

	var stdoutBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to open stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	var stderrBuf bytes.Buffer
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		scanner := bufio.NewScanner(stderrPipe)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			if stderrBuf.Len() < maxStderrCapture {
				if stderrBuf.Len() > 0 {
					stderrBuf.WriteByte('\n')
				}
				stderrBuf.WriteString(line)
			}
			if onLog != nil {
				onLog(line)
			}
		}
		if sErr := scanner.Err(); sErr != nil && onLog != nil {
			onLog(fmt.Sprintf("[stderr-scan-error] %v", sErr))
		}
	}()

	<-stderrDone
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		errMsg := ExtractLastError(stderrBuf.String())
		if errMsg == "" {
			errMsg = err.Error()
		}
		return nil, errors.New(errMsg)
	}

	return stdoutBuf.Bytes(), nil
}

// FetchMetadata queries yt-dlp for video metadata and stream formats.
func FetchMetadata(ctx context.Context, binPath string, videoURL string) (*VideoMetadata, error) {
	return FetchMetadataWithOptions(ctx, binPath, videoURL, MetadataOptions{}, nil)
}

// FetchMetadataWithLogs queries yt-dlp for video metadata while streaming stderr CLI logs.
func FetchMetadataWithLogs(ctx context.Context, binPath string, videoURL string, onLog func(string)) (*VideoMetadata, error) {
	return FetchMetadataWithOptions(ctx, binPath, videoURL, MetadataOptions{}, onLog)
}

// FetchMetadataWithOptions queries yt-dlp for video metadata with optional auth/proxy settings.
func FetchMetadataWithOptions(ctx context.Context, binPath string, videoURL string, opts MetadataOptions, onLog func(string)) (*VideoMetadata, error) {
	sanitizedURL, err := ParseURL(videoURL)
	if err != nil {
		return nil, err
	}
	if binPath == "" {
		return nil, ErrMissingBinary
	}

	args := appendMetadataOptions([]string{"--dump-single-json", "--no-playlist"}, opts)
	args = append(args, "--", sanitizedURL)
	raw, err := runMetadataCommand(ctx, binPath, args, onLog)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fmt.Errorf("metadata fetch failed: %w", err)
	}

	var meta VideoMetadata
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, fmt.Errorf("failed to parse video metadata: %w", err)
	}

	if meta.Channel == "" && meta.Uploader != "" {
		meta.Channel = meta.Uploader
	}
	if meta.DurationString == "" && meta.Duration > 0 {
		m := int(meta.Duration) / 60
		s := int(meta.Duration) % 60
		meta.DurationString = fmt.Sprintf("%d:%02d", m, s)
	}

	return &meta, nil
}

func shortVideoCodec(vcodec string) string {
	l := strings.ToLower(vcodec)
	switch {
	case strings.HasPrefix(l, "avc1"), strings.Contains(l, "h264"):
		return "H.264"
	case strings.HasPrefix(l, "vp09"), strings.HasPrefix(l, "vp9"):
		return "VP9"
	case strings.HasPrefix(l, "av01"), strings.Contains(l, "av1"):
		return "AV1"
	case strings.HasPrefix(l, "hev1"), strings.HasPrefix(l, "hvc1"), strings.Contains(l, "h265"):
		return "HEVC"
	default:
		return ""
	}
}

func shortAudioCodec(acodec string) string {
	l := strings.ToLower(acodec)
	switch {
	case strings.Contains(l, "opus"):
		return "Opus"
	case strings.HasPrefix(l, "mp4a"), strings.Contains(l, "aac"):
		return "AAC"
	case strings.Contains(l, "vorbis"):
		return "Vorbis"
	case strings.Contains(l, "flac"):
		return "FLAC"
	default:
		return ""
	}
}

// ExtractVideoOptions extracts sorted video stream options from metadata formats.
func ExtractVideoOptions(formats []StreamFormat) []FormatOption {
	options := []FormatOption{
		{Label: "Auto (Best Video)", Value: "bestvideo"},
	}

	type item struct {
		f StreamFormat
	}
	var videos []item
	for _, f := range formats {
		if f.VCodec != "" && f.VCodec != "none" && f.Height > 0 {
			videos = append(videos, item{f: f})
		}
	}

	sort.Slice(videos, func(i, j int) bool {
		if videos[i].f.Height != videos[j].f.Height {
			return videos[i].f.Height > videos[j].f.Height
		}
		return videos[i].f.FPS > videos[j].f.FPS
	})

	seen := make(map[string]bool)
	for _, v := range videos {
		f := v.f
		fpsStr := ""
		if f.FPS > 30 {
			fpsStr = fmt.Sprintf("%.0f", f.FPS)
		}
		sizeStr := ""
		size := f.Filesize
		if size == 0 {
			size = f.FilesizeApprox
		}
		if size > 0 {
			sizeStr = fmt.Sprintf(" · %.1f MB", float64(size)/1024/1024)
		}
		codec := shortVideoCodec(f.VCodec)
		codecStr := ""
		if codec != "" {
			codecStr = " · " + codec
		}
		label := fmt.Sprintf("%dp%s (%s%s)%s", f.Height, fpsStr, f.Ext, codecStr, sizeStr)
		if !seen[label] {
			seen[label] = true
			options = append(options, FormatOption{Label: label, Value: f.FormatID})
		}
	}

	options = append(options, FormatOption{Label: "Audio Only (No Video)", Value: "none"})
	return options
}

// ExtractAudioOptions extracts sorted audio stream options from metadata formats.
func ExtractAudioOptions(formats []StreamFormat) []FormatOption {
	options := []FormatOption{
		{Label: "Auto (Best Audio)", Value: "bestaudio"},
	}

	type item struct {
		f StreamFormat
	}
	var audios []item
	for _, f := range formats {
		if (f.VCodec == "" || f.VCodec == "none") && f.ACodec != "" && f.ACodec != "none" {
			audios = append(audios, item{f: f})
		}
	}

	sort.Slice(audios, func(i, j int) bool {
		return audios[i].f.ABR > audios[j].f.ABR
	})

	seen := make(map[string]bool)
	for _, a := range audios {
		f := a.f
		abrStr := ""
		if f.ABR > 0 {
			abrStr = fmt.Sprintf("%.0fkbps ", f.ABR)
		}
		sizeStr := ""
		size := f.Filesize
		if size == 0 {
			size = f.FilesizeApprox
		}
		if size > 0 {
			sizeStr = fmt.Sprintf(" · %.1f MB", float64(size)/1024/1024)
		}
		codec := shortAudioCodec(f.ACodec)
		codecStr := ""
		if codec != "" {
			codecStr = " · " + codec
		}
		label := fmt.Sprintf("%s(%s%s)%s", abrStr, f.Ext, codecStr, sizeStr)
		if !seen[label] {
			seen[label] = true
			options = append(options, FormatOption{Label: label, Value: f.FormatID})
		}
	}

	options = append(options, FormatOption{Label: "None (Muted)", Value: "none"})
	return options
}

// PlaylistEntry describes one item returned by yt-dlp --flat-playlist.
type PlaylistEntry struct {
	ID             string  `json:"id"`
	Title          string  `json:"title"`
	Uploader       string  `json:"uploader"`
	Channel        string  `json:"channel"`
	Duration       float64 `json:"duration"`
	DurationString string  `json:"duration_string"`
	URL            string  `json:"url"`
	WebpageURL     string  `json:"webpage_url"`
	Index          int     `json:"playlist_index"`
}

// PlaylistMetadata describes a playlist or channel returned by yt-dlp --flat-playlist.
type PlaylistMetadata struct {
	ID       string          `json:"id"`
	Title    string          `json:"title"`
	Channel  string          `json:"channel"`
	Uploader string          `json:"uploader"`
	Entries  []PlaylistEntry `json:"entries"`
}

// ParsePlaylistMetadataJSON parses yt-dlp --flat-playlist --dump-single-json output.
func ParsePlaylistMetadataJSON(raw []byte) (*PlaylistMetadata, error) {
	var pm PlaylistMetadata
	if err := json.Unmarshal(raw, &pm); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPlaylistFetchFailed, err)
	}
	if pm.Channel == "" && pm.Uploader != "" {
		pm.Channel = pm.Uploader
	}
	for i := range pm.Entries {
		e := &pm.Entries[i]
		if e.Index == 0 {
			e.Index = i + 1
		}
		if e.Channel == "" && e.Uploader != "" {
			e.Channel = e.Uploader
		}
		if e.URL == "" && e.WebpageURL != "" {
			e.URL = e.WebpageURL
		} else if e.URL == "" && e.ID != "" {
			e.URL = "https://www.youtube.com/watch?v=" + e.ID
		} else if e.URL != "" && !strings.HasPrefix(e.URL, "http") && e.ID != "" {
			e.URL = "https://www.youtube.com/watch?v=" + e.ID
		}
		if e.DurationString == "" && e.Duration > 0 {
			m := int(e.Duration) / 60
			s := int(e.Duration) % 60
			e.DurationString = fmt.Sprintf("%d:%02d", m, s)
		}
	}
	if len(pm.Entries) == 0 {
		return nil, fmt.Errorf("%w: no entries found", ErrPlaylistFetchFailed)
	}
	return &pm, nil
}

// FetchPlaylistMetadata queries yt-dlp for flat playlist entries.
func FetchPlaylistMetadata(ctx context.Context, binPath, playlistURL string) (*PlaylistMetadata, error) {
	return FetchPlaylistMetadataWithOptions(ctx, binPath, playlistURL, MetadataOptions{}, nil)
}

// FetchPlaylistMetadataWithLogs queries yt-dlp for flat playlist entries while streaming stderr CLI logs.
func FetchPlaylistMetadataWithLogs(ctx context.Context, binPath, playlistURL string, onLog func(string)) (*PlaylistMetadata, error) {
	return FetchPlaylistMetadataWithOptions(ctx, binPath, playlistURL, MetadataOptions{}, onLog)
}

// FetchPlaylistMetadataWithOptions queries yt-dlp for flat playlist entries with optional auth/proxy settings.
func FetchPlaylistMetadataWithOptions(ctx context.Context, binPath, playlistURL string, opts MetadataOptions, onLog func(string)) (*PlaylistMetadata, error) {
	sanitizedURL, err := ParseURL(playlistURL)
	if err != nil {
		return nil, err
	}
	if binPath == "" {
		return nil, ErrMissingBinary
	}

	args := appendMetadataOptions([]string{"--flat-playlist", "--dump-single-json"}, opts)
	args = append(args, "--", sanitizedURL)
	raw, err := runMetadataCommand(ctx, binPath, args, onLog)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %v", ErrPlaylistFetchFailed, err)
	}

	return ParsePlaylistMetadataJSON(raw)
}

// UpdateBinary runs yt-dlp -U to update the yt-dlp executable in place.
func UpdateBinary(ctx context.Context, binPath string) (string, error) {
	if binPath == "" {
		return "", ErrMissingBinary
	}
	cmd := exec.CommandContext(ctx, binPath, "-U")
	prepareCommand(cmd)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		msg := ExtractLastError(errBuf.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("yt-dlp update failed: %s", msg)
	}
	return stringsTrim(outBuf.String()), nil
}
