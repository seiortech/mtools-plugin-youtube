package youtube

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/seiortech/mtools-sdk/job"
)

func TestParseOutputLine(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantPath string
		wantOk   bool
	}{
		{
			name:     "download destination",
			input:    "[download] Destination: /path/to/video.webm",
			wantPath: "/path/to/video.webm",
			wantOk:   true,
		},
		{
			name:     "merger destination quoted",
			input:    `[Merger] Merging formats into "/path/to/video.mp4"`,
			wantPath: "/path/to/video.mp4",
			wantOk:   true,
		},
		{
			name:     "merger destination unquoted",
			input:    `[Merger] Merging formats into /path/to/video.mp4`,
			wantPath: "/path/to/video.mp4",
			wantOk:   true,
		},
		{
			name:     "fixup m3u8",
			input:    `[FixupM3u8] Merging formats into "/path/to/stream.mp4"`,
			wantPath: "/path/to/stream.mp4",
			wantOk:   true,
		},
		{
			name:     "extract audio destination",
			input:    "[ExtractAudio] Destination: /path/to/audio.mp3",
			wantPath: "/path/to/audio.mp3",
			wantOk:   true,
		},
		{
			name:     "already downloaded",
			input:    "[download] /path/to/existing.mp4 has already been downloaded",
			wantPath: "/path/to/existing.mp4",
			wantOk:   true,
		},
		{
			name:     "download percentage progress line must not match",
			input:    "[download] 100% of 14.20MiB in 00:00:02 at 5.12MiB/s",
			wantPath: "",
			wantOk:   false,
		},
		{
			name:     "intermediate download progress line",
			input:    "[download]  45.0% of ~20.00MiB at 4.50MiB/s ETA 00:02",
			wantPath: "",
			wantOk:   false,
		},
		{
			name:     "unrelated yt-dlp log line",
			input:    "[info] C4Bjxf14BMg: Downloading webpage",
			wantPath: "",
			wantOk:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotPath, gotOk := parseOutputLine(tc.input)
			if gotOk != tc.wantOk {
				t.Fatalf("parseOutputLine(%q) ok = %v, want %v", tc.input, gotOk, tc.wantOk)
			}
			if gotPath != tc.wantPath {
				t.Fatalf("parseOutputLine(%q) path = %q, want %q", tc.input, gotPath, tc.wantPath)
			}
		})
	}
}

func TestExpandOutputDir(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("UserHomeDir unavailable:", err)
	}

	if got := expandOutputDir(""); got != filepath.Join(home, "Downloads") {
		t.Errorf("expandOutputDir(\"\") = %q, want %q", got, filepath.Join(home, "Downloads"))
	}
	if got := expandOutputDir("~/Downloads"); got != filepath.Join(home, "Downloads") {
		t.Errorf("expandOutputDir(\"~/Downloads\") = %q, want %q", got, filepath.Join(home, "Downloads"))
	}
	if got := expandOutputDir("~/Videos/YT"); got != filepath.Join(home, "Videos/YT") {
		t.Errorf("expandOutputDir(\"~/Videos/YT\") = %q, want %q", got, filepath.Join(home, "Videos/YT"))
	}
	if got := expandOutputDir("/custom/dir"); got != "/custom/dir" {
		t.Errorf("expandOutputDir(\"/custom/dir\") = %q, want \"/custom/dir\"", got)
	}
}

func TestResolveFinalOutputPath(t *testing.T) {
	tmp := t.TempDir()

	// 1. Existing file returned as is
	realFile := filepath.Join(tmp, "song.webm")
	if err := os.WriteFile(realFile, []byte("audio"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := resolveFinalOutputPath(tmp, "song.webm", ""); got != realFile {
		t.Errorf("resolveFinalOutputPath for existing file = %q, want %q", got, realFile)
	}

	// 2. Intermediate stream merged to OutputFormat
	mergedFile := filepath.Join(tmp, "song_merged.mp4")
	if err := os.WriteFile(mergedFile, []byte("video"), 0644); err != nil {
		t.Fatal(err)
	}
	// Pass an unmerged stream name that does not exist on disk
	gotMerged := resolveFinalOutputPath(tmp, "song_merged.f251.webm", "mp4")
	if gotMerged != mergedFile {
		t.Errorf("resolveFinalOutputPath for merged stream = %q, want %q", gotMerged, mergedFile)
	}

	// 3. Intermediate stream merged to common container when format not specified
	mergedMkv := filepath.Join(tmp, "stream.mkv")
	if err := os.WriteFile(mergedMkv, []byte("video"), 0644); err != nil {
		t.Fatal(err)
	}
	gotMkv := resolveFinalOutputPath(tmp, "stream.f137.webm", "")
	if gotMkv != mergedMkv {
		t.Errorf("resolveFinalOutputPath fallback to mkv = %q, want %q", gotMkv, mergedMkv)
	}
}

func TestDownloadJobStreamsStdoutAndStderrLogs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fixture requires POSIX shell")
	}

	tmp := t.TempDir()
	bin := filepath.Join(tmp, "yt-dlp")
	outFile := filepath.Join(tmp, "video.mp4")
	if err := os.WriteFile(outFile, []byte("ok"), 0644); err != nil {
		t.Fatal(err)
	}

	script := `#!/bin/sh
echo "[youtube] Extracting URL: https://www.youtube.com/watch?v=abc"
echo "WARNING: ffmpeg stderr notice" >&2
echo "[download] Destination: ` + outFile + `"
echo "100.0%|10.0MiB/s|00:00"
`
	if err := os.WriteFile(bin, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var logs []string
	var lastMsg string
	dl := DownloadJob{
		URL:        "https://www.youtube.com/watch?v=abc",
		BinaryPath: bin,
		OutputDir:  tmp,
	}
	res, err := dl.Run(context.Background(), func(p job.Progress) {
		mu.Lock()
		if p.LogLine != "" {
			logs = append(logs, p.LogLine)
		}
		if p.Message != "" {
			lastMsg = p.Message
		}
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("DownloadJob.Run failed: %v", err)
	}
	if len(res.Outputs) == 0 || res.Outputs[0] != outFile {
		t.Fatalf("unexpected outputs: %+v", res.Outputs)
	}

	joined := strings.Join(logs, "\n")
	for _, want := range []string{
		"[cmd] ",
		"[youtube] Extracting URL",
		"WARNING: ffmpeg stderr notice",
		"100.0%|10.0MiB/s|00:00",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected log %q in %q", want, joined)
		}
	}
	if lastMsg == "" {
		t.Errorf("expected non-empty progress message")
	}
}
