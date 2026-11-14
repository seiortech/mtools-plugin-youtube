package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"runtime"
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
