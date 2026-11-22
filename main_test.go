package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/seiortech/mtools-sdk/pluginapi"
)

func TestManifestAndSurfacesValid(t *testing.T) {
	mBytes, err := os.ReadFile("manifest.json")
	if err != nil {
		t.Fatalf("read manifest.json: %v", err)
	}
	var manifest pluginapi.Manifest
	if err := json.Unmarshal(mBytes, &manifest); err != nil {
		t.Fatalf("unmarshal manifest.json: %v", err)
	}

	sBytes, err := os.ReadFile("surfaces.json")
	if err != nil {
		t.Fatalf("read surfaces.json: %v", err)
	}
	sSum := sha256.Sum256(sBytes)

	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	manifest.Platform = runtime.GOOS + "-" + runtime.GOARCH
	manifest.Publisher.PublicKey = base64.StdEncoding.EncodeToString(pub)
	manifest.Files = map[string]pluginapi.FileInfo{
		"bin/tool": {
			SHA256:     hex.EncodeToString(sSum[:]),
			Size:       1,
			Executable: true,
		},
		"surfaces.json": {
			SHA256:     hex.EncodeToString(sSum[:]),
			Size:       int64(len(sBytes)),
			Executable: false,
		},
	}

	doc, err := pluginapi.DecodeSurfaces(sBytes, manifest)
	if err != nil {
		t.Fatalf("DecodeSurfaces failed: %v", err)
	}

	var hasFetchFFmpeg bool
	for _, a := range doc.Tools[0].Actions {
		if a.ID == "fetch_ffmpeg" {
			hasFetchFFmpeg = true
			break
		}
	}
	if !hasFetchFFmpeg {
		t.Fatalf("expected fetch_ffmpeg action in surfaces.json")
	}

	var hasFFmpegSetting bool
	for _, s := range doc.Tools[0].Settings {
		if s.ID == "ffmpeg_path" && s.Action != nil && s.Action.ID == "fetch_ffmpeg" {
			hasFFmpegSetting = true
			break
		}
	}
	if !hasFFmpegSetting {
		t.Fatalf("expected ffmpeg_path setting with fetch_ffmpeg action in surfaces.json")
	}
}

func TestBatchAutoContinueOnItemFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fixture requires POSIX shell")
	}

	tmp := t.TempDir()
	bin := filepath.Join(tmp, "yt-dlp")
	out1 := filepath.Join(tmp, "item1.mp4")
	out3 := filepath.Join(tmp, "item3.mp4")

	script := `#!/bin/sh
last=""
for a in "$@"; do
  last="$a"
done
case "$last" in
  *v=item1*)
    echo "ok" > "` + out1 + `"
    echo "[download] Destination: ` + out1 + `"
    echo "100.0%|10.0MiB/s|00:00"
    exit 0
    ;;
  *v=item2*)
    echo "ERROR: Private video" >&2
    exit 1
    ;;
  *v=item3*)
    echo "ok" > "` + out3 + `"
    echo "[download] Destination: ` + out3 + `"
    echo "100.0%|10.0MiB/s|00:00"
    exit 0
    ;;
esac
exit 1
`
	if err := os.WriteFile(bin, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	srv := newYTServer()
	res, err := srv.Run(context.Background(), pluginapi.RunParams{
		JobID:  "job-batch-1",
		Tool:   "youtube",
		Action: "download",
		Inputs: map[string][]string{
			"url":               {"https://www.youtube.com/playlist?list=PL123"},
			"output_dir":        {tmp},
			"continue_on_error": {"true"},
			"playlist_items": {
				"https://www.youtube.com/watch?v=item1",
				"https://www.youtube.com/watch?v=item2",
				"https://www.youtube.com/watch?v=item3",
			},
		},
		Settings: map[string]string{
			"binary_path": bin,
		},
	}, func(pluginapi.Progress) error { return nil })
	if err != nil {
		t.Fatalf("expected batch download to auto-continue past item2 failure, got error: %v", err)
	}
	if len(res.Outputs) != 2 {
		t.Fatalf("expected 2 outputs from 3-item batch, got %d: %+v", len(res.Outputs), res.Outputs)
	}
	if !strings.Contains(res.Summary, "2/3") || !strings.Contains(res.Summary, "1 failed") {
		t.Fatalf("expected partial failure summary, got %q", res.Summary)
	}
}

func TestInspectMetadataCache(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fixture requires POSIX shell")
	}

	tmp := t.TempDir()
	bin := filepath.Join(tmp, "yt-dlp")
	countFile := filepath.Join(tmp, "invocations.txt")

	script := `#!/bin/sh
echo "1" >> "` + countFile + `"
echo '{"id":"abc","title":"Cached Video","channel":"Test Channel","duration_string":"1:23","formats":[]}'
`
	if err := os.WriteFile(bin, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	srv := newYTServer()
	params := pluginapi.InspectParams{
		Tool:   "youtube",
		Action: "preview",
		Inputs: map[string][]string{
			"url": {"https://www.youtube.com/watch?v=abc"},
		},
		Settings: map[string]string{
			"binary_path": bin,
		},
	}

	res1, err := srv.Inspect(context.Background(), params)
	if err != nil || res1.Error != "" {
		t.Fatalf("first Inspect failed: err=%v res.Error=%q", err, res1.Error)
	}
	res2, err := srv.Inspect(context.Background(), params)
	if err != nil || res2.Error != "" {
		t.Fatalf("second Inspect failed: err=%v res.Error=%q", err, res2.Error)
	}
	if res2.Preview == nil || res2.Preview.Title != "Cached Video" {
		t.Fatalf("expected cached preview title, got %+v", res2.Preview)
	}

	raw, err := os.ReadFile(countFile)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(strings.TrimSpace(string(raw)), "\n") + 1; lines != 1 {
		t.Fatalf("expected yt-dlp to be invoked once due to cache, got %d times", lines)
	}
}
