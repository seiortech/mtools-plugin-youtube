package youtube

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestParseURL(t *testing.T) {
	valid := []string{
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		"http://youtube.com/watch?v=dQw4w9WgXcQ",
		"https://m.youtube.com/watch?v=dQw4w9WgXcQ",
		"https://youtu.be/dQw4w9WgXcQ",
	}

	for _, u := range valid {
		got, err := ParseURL(u)
		if err != nil {
			t.Errorf("expected %q to be valid, got error: %v", u, err)
		}
		if got != u {
			t.Errorf("expected %q, got %q", u, got)
		}
	}

	invalid := []string{
		"",
		"not-a-url",
		"ftp://youtube.com/watch?v=dQw4w9WgXcQ",
		"https://vimeo.com/12345",
		"https://malicious-youtube.com/watch",
	}

	for _, u := range invalid {
		_, err := ParseURL(u)
		if !errors.Is(err, ErrInvalidURL) {
			t.Errorf("expected ErrInvalidURL for %q, got: %v", u, err)
		}
	}
}

func TestParseProgressLine(t *testing.T) {
	line := " 45.2%|12.1MiB/s|00:15"
	p, ok := ParseProgressLine(line)
	if !ok {
		t.Fatalf("expected progress line to parse")
	}
	if p.Done != 45.2 {
		t.Errorf("expected Done == 45.2, got %v", p.Done)
	}
	if p.Unit != "%" {
		t.Errorf("expected Unit '%%', got %q", p.Unit)
	}
	if p.Message != "12.1MiB/s · ETA 00:15" {
		t.Errorf("expected message '12.1MiB/s · ETA 00:15', got %q", p.Message)
	}

	// Fallback format
	fbLine := "[download]  75.0% of 100.00MiB at 10.00MiB/s"
	p2, ok2 := ParseProgressLine(fbLine)
	if !ok2 {
		t.Fatalf("expected fallback line to parse")
	}
	if p2.Done != 75.0 {
		t.Errorf("expected Done == 75.0, got %v", p2.Done)
	}
}

func TestResolveBinary(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mtools-youtube-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Custom file
	customFile := filepath.Join(tempDir, "custom-yt-dlp")
	if err := os.WriteFile(customFile, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatalf("failed to write custom file: %v", err)
	}

	resolved, err := ResolveBinary(customFile, "")
	if err != nil || resolved != customFile {
		t.Fatalf("expected resolved %q, got %q (err: %v)", customFile, resolved, err)
	}

	// Managed binary in data dir
	binDir := filepath.Join(tempDir, "data", "bin")
	_ = os.MkdirAll(binDir, 0755)
	managedFile := filepath.Join(binDir, BinaryName())
	if err := os.WriteFile(managedFile, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatalf("failed to write managed file: %v", err)
	}

	resolvedManaged, err := ResolveBinary("", filepath.Join(tempDir, "data"))
	if err != nil || resolvedManaged != managedFile {
		t.Fatalf("expected resolved %q, got %q (err: %v)", managedFile, resolvedManaged, err)
	}
}

func TestBuildArgsAndCommandString(t *testing.T) {
	opts := CommandOptions{
		BinaryPath:   "/usr/local/bin/yt-dlp",
		URL:          "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		OutputDir:    "/Users/raden/Downloads",
		VideoStream:  "137",
		AudioStream:  "140",
		OutputFormat: "mp4",
		EmbedSubs:    true,
	}

	args := BuildArgs(opts)
	// Verify args contain destination, format, and embed-subs
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-P /Users/raden/Downloads") {
		t.Errorf("expected -P flag in args, got %s", joined)
	}
	if !strings.Contains(joined, "-f 137+140") {
		t.Errorf("expected -f 137+140 in args, got %s", joined)
	}
	if !strings.Contains(joined, "--merge-output-format mp4") {
		t.Errorf("expected --merge-output-format mp4, got %s", joined)
	}
	if !strings.Contains(joined, "--embed-subs") {
		t.Errorf("expected --embed-subs, got %s", joined)
	}

	cmdStr := BuildCommandString(opts)
	if !strings.HasPrefix(cmdStr, "/usr/local/bin/yt-dlp") {
		t.Errorf("expected command string to start with binary, got %s", cmdStr)
	}
	if !strings.Contains(cmdStr, "--merge-output-format mp4") {
		t.Errorf("expected command string to include merge format, got %s", cmdStr)
	}
}

func TestBuildArgsCustomFilename(t *testing.T) {
	// 1. Plain name
	args1 := BuildArgs(CommandOptions{
		CustomFilename: "my_custom_video",
		URL:            "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
	})
	joined1 := strings.Join(args1, " ")
	if !strings.Contains(joined1, "-o my_custom_video.%(ext)s") {
		t.Errorf("expected -o my_custom_video.%%(ext)s, got: %s", joined1)
	}

	// 2. Name with extension
	args2 := BuildArgs(CommandOptions{
		CustomFilename: "awesome_song.mp4",
		URL:            "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
	})
	joined2 := strings.Join(args2, " ")
	if !strings.Contains(joined2, "-o awesome_song.%(ext)s") {
		t.Errorf("expected -o awesome_song.%%(ext)s, got: %s", joined2)
	}

	// 3. Name with yt-dlp template
	args3 := BuildArgs(CommandOptions{
		CustomFilename: "%(title)s [%(id)s].%(ext)s",
		URL:            "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
	})
	joined3 := strings.Join(args3, " ")
	if !strings.Contains(joined3, "-o %(title)s [%(id)s].%(ext)s") {
		t.Errorf("expected -o %%(title)s [%%(id)s].%%(ext)s, got: %s", joined3)
	}

	// 4. Empty name
	args4 := BuildArgs(CommandOptions{
		URL: "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
	})
	joined4 := strings.Join(args4, " ")
	if strings.Contains(joined4, "-o") {
		t.Errorf("expected no -o flag for empty custom filename, got: %s", joined4)
	}
}

func TestExtractStreamOptions(t *testing.T) {
	formats := []StreamFormat{
		{FormatID: "137", Height: 1080, FPS: 30, Ext: "mp4", VCodec: "avc1", ACodec: "none", Filesize: 100 * 1024 * 1024},
		{FormatID: "248", Height: 1080, FPS: 60, Ext: "webm", VCodec: "vp9", ACodec: "none", Filesize: 120 * 1024 * 1024},
		{FormatID: "136", Height: 720, FPS: 30, Ext: "mp4", VCodec: "avc1", ACodec: "none", Filesize: 50 * 1024 * 1024},
		{FormatID: "140", Ext: "m4a", VCodec: "none", ACodec: "mp4a", ABR: 128, Filesize: 5 * 1024 * 1024},
		{FormatID: "251", Ext: "webm", VCodec: "none", ACodec: "opus", ABR: 160, Filesize: 6 * 1024 * 1024},
	}

	videoOpts := ExtractVideoOptions(formats)
	if len(videoOpts) < 4 {
		t.Fatalf("expected at least 4 video options, got %d", len(videoOpts))
	}
	if videoOpts[0].Value != "bestvideo" {
		t.Errorf("expected first video option 'bestvideo', got %s", videoOpts[0].Value)
	}
	if videoOpts[len(videoOpts)-1].Value != "none" {
		t.Errorf("expected last video option 'none', got %s", videoOpts[len(videoOpts)-1].Value)
	}

	audioOpts := ExtractAudioOptions(formats)
	if len(audioOpts) < 3 {
		t.Fatalf("expected at least 3 audio options, got %d", len(audioOpts))
	}
	if audioOpts[0].Value != "bestaudio" {
		t.Errorf("expected first audio option 'bestaudio', got %s", audioOpts[0].Value)
	}
	if audioOpts[len(audioOpts)-1].Value != "none" {
		t.Errorf("expected last audio option 'none', got %s", audioOpts[len(audioOpts)-1].Value)
	}
}

func TestParseTimeRange(t *testing.T) {
	if got, err := ParseTimeRange("", ""); err != nil || got != "" {
		t.Fatalf("expected empty range, got %q, err=%v", got, err)
	}
	if got, err := ParseTimeRange("00:01:30", "00:02:45"); err != nil || got != "*00:01:30-00:02:45" {
		t.Fatalf("expected *00:01:30-00:02:45, got %q, err=%v", got, err)
	}
	if got, err := ParseTimeRange("10", ""); err != nil || got != "*10-inf" {
		t.Fatalf("expected *10-inf, got %q, err=%v", got, err)
	}
	if _, err := ParseTimeRange("00:05:00", "00:01:00"); err != ErrInvalidTimeRange {
		t.Fatalf("expected ErrInvalidTimeRange for start>=end, got %v", err)
	}
	if _, err := ParseTimeRange("bad", "00:01:00"); err != ErrInvalidTimeRange {
		t.Fatalf("expected ErrInvalidTimeRange for invalid input, got %v", err)
	}
}

func TestParseRateLimitAndProxy(t *testing.T) {
	if got, err := ParseRateLimit("5M"); err != nil || got != "5M" {
		t.Fatalf("expected 5M, got %q, err=%v", got, err)
	}
	if _, err := ParseRateLimit("fast"); err != ErrInvalidRateLimit {
		t.Fatalf("expected ErrInvalidRateLimit, got %v", err)
	}
	if got, err := ParseProxyURL("socks5://127.0.0.1:1080"); err != nil || got != "socks5://127.0.0.1:1080" {
		t.Fatalf("expected valid proxy, got %q, err=%v", got, err)
	}
	if _, err := ParseProxyURL("ftp://bad:21"); err != ErrInvalidProxyURL {
		t.Fatalf("expected ErrInvalidProxyURL, got %v", err)
	}
}

func TestBuildArgsAdvancedAndRedaction(t *testing.T) {
	opts := CommandOptions{
		BinaryPath:          "yt-dlp",
		FFmpegPath:          "/usr/local/bin/ffmpeg",
		URL:                 "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		OutputDir:           "/tmp/out",
		VideoStream:         "none",
		AudioStream:         "bestaudio",
		OutputFormat:        "opus",
		AudioQuality:        "192K",
		EmbedSubs:           true,
		SubLangs:            "en,id",
		WriteAutoSubs:       true,
		EmbedThumbnail:      true,
		EmbedChapters:       true,
		EmbedMetadata:       true,
		SponsorBlockMode:    "remove",
		SponsorBlockCats:    []string{"sponsor", "intro"},
		TimeRangeStart:      "00:00:10",
		TimeRangeEnd:        "00:00:30",
		ForceKeyframesCuts:  true,
		CookiesFile:         "/secret/cookies.txt",
		ProxyURL:            "http://user:pass@127.0.0.1:8080",
		RateLimit:           "5M",
		ConcurrentFragments: 4,
	}

	args := BuildArgs(opts)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--ffmpeg-location /usr/local/bin",
		"--audio-format opus",
		"--audio-quality 192K",
		"--embed-subs",
		"--sub-langs en,id",
		"--write-auto-subs",
		"--embed-thumbnail",
		"--embed-chapters",
		"--embed-metadata",
		"--sponsorblock-remove sponsor,intro",
		"--download-sections *00:00:10-00:00:30",
		"--force-keyframes-at-cuts",
		"--cookies /secret/cookies.txt",
		"--proxy http://user:pass@127.0.0.1:8080",
		"--limit-rate 5M",
		"-N 4",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected args to contain %q, got: %s", want, joined)
		}
	}

	preview := BuildCommandString(opts)
	if strings.Contains(preview, "/secret/cookies.txt") || strings.Contains(preview, "pass") {
		t.Errorf("expected BuildCommandString to redact secrets, got: %s", preview)
	}
	if !strings.Contains(preview, "[REDACTED]") {
		t.Errorf("expected [REDACTED] marker in preview, got: %s", preview)
	}
}

func TestParsePlaylistMetadataJSON(t *testing.T) {
	raw := []byte(`{
		"id": "PL123",
		"title": "Go Talks",
		"uploader": "GopherCon",
		"entries": [
			{"id": "abc", "title": "Concurrency", "duration": 125},
			{"id": "def", "title": "Generics", "duration": 60, "url": "https://www.youtube.com/watch?v=def"}
		]
	}`)
	pm, err := ParsePlaylistMetadataJSON(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pm.Title != "Go Talks" || len(pm.Entries) != 2 {
		t.Fatalf("unexpected playlist metadata: %+v", pm)
	}
	if pm.Entries[0].URL != "https://www.youtube.com/watch?v=abc" || pm.Entries[0].DurationString != "2:05" {
		t.Errorf("unexpected normalized first entry: %+v", pm.Entries[0])
	}
}

func TestFetchMetadataWithLogs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fixture requires POSIX shell")
	}

	tmp := t.TempDir()
	bin := filepath.Join(tmp, "yt-dlp")
	script := `#!/bin/sh
echo "[youtube] Extracting URL: https://www.youtube.com/watch?v=abc" >&2
echo "WARNING: sample warning" >&2
echo '{"id":"abc","title":"Test Video","uploader":"TestChannel","duration":90,"formats":[]}'
`
	if err := os.WriteFile(bin, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var logs []string
	meta, err := FetchMetadataWithLogs(context.Background(), bin, "https://www.youtube.com/watch?v=abc", func(line string) {
		mu.Lock()
		logs = append(logs, line)
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("FetchMetadataWithLogs failed: %v", err)
	}
	if meta.Title != "Test Video" || meta.Channel != "TestChannel" {
		t.Fatalf("unexpected metadata: %+v", meta)
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "[youtube] Extracting URL") || !strings.Contains(joined, "WARNING: sample warning") {
		t.Fatalf("expected stderr logs captured, got: %q", joined)
	}
}

func TestFetchPlaylistMetadataWithLogs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fixture requires POSIX shell")
	}

	tmp := t.TempDir()
	bin := filepath.Join(tmp, "yt-dlp")
	script := `#!/bin/sh
echo "[youtube:tab] Extracting URL: playlist" >&2
echo '{"id":"pl1","title":"Test Playlist","entries":[{"id":"v1","title":"Item 1","duration":60}]}'
`
	if err := os.WriteFile(bin, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var logs []string
	pm, err := FetchPlaylistMetadataWithLogs(context.Background(), bin, "https://www.youtube.com/playlist?list=pl1", func(line string) {
		mu.Lock()
		logs = append(logs, line)
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("FetchPlaylistMetadataWithLogs failed: %v", err)
	}
	if pm.Title != "Test Playlist" || len(pm.Entries) != 1 {
		t.Fatalf("unexpected playlist metadata: %+v", pm)
	}
	if len(logs) == 0 || !strings.Contains(logs[0], "[youtube:tab] Extracting URL") {
		t.Fatalf("expected playlist stderr log, got: %v", logs)
	}
}
