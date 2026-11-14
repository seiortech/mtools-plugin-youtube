package youtube

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/seiortech/mtools-sdk/job"
	plugin "github.com/seiortech/mtools-sdk/settings"
)

func TestWorkspaceMissingBinary(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mtools-youtube-view-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	settingsFile := filepath.Join(tempDir, "settings.json")
	store, _ := plugin.NewJSONSettingsStore(settingsFile, PluginSettingsSchema())
	store.Set("binary_path", "/non-existent/path/yt-dlp")

	origLookPath := lookPath
	lookPath = func(file string) (string, error) {
		return "", errors.New("not found")
	}
	defer func() { lookPath = origLookPath }()

	q := job.NewQueue(1, nil)
	p := NewPlugin(tempDir, store, q)

	tt := ui.NewTester(p.View, 800, 600)
	tt.Frame()

	// Should show missing binary card
	if !tt.HasText("Engine Executable Missing") {
		t.Errorf("expected 'Engine Executable Missing' warning card")
	}
	if !tt.HasText("Store in Default Path") {
		t.Errorf("expected 'Store in Default Path' action button")
	}
	if !tt.HasText("Download yt-dlp...") {
		t.Errorf("expected 'Download yt-dlp...' action button")
	}
	if p.GetDefaultPath() != tempDir {
		t.Errorf("expected GetDefaultPath %q, got %q", tempDir, p.GetDefaultPath())
	}
}

func TestWorkspaceJobCardWithLogs(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mtools-youtube-view-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dummyBin := filepath.Join(tempDir, "bin", BinaryName())
	_ = os.MkdirAll(filepath.Dir(dummyBin), 0755)
	_ = os.WriteFile(dummyBin, []byte("#!/bin/sh\n"), 0755)

	settingsFile := filepath.Join(tempDir, "settings.json")
	store, _ := plugin.NewJSONSettingsStore(settingsFile, PluginSettingsSchema())
	store.Set("binary_path", dummyBin)

	q := job.NewQueue(1, nil)
	p := NewPlugin(tempDir, store, q)

	// Enqueue dummy job and inject test log lines
	doneCh := make(chan struct{})
	jobID := q.Enqueue("YouTube Downloader", job.Func(func(ctx context.Context, progress func(job.Progress)) (job.Result, error) {
		progress(job.Progress{Done: 50, LogLine: "[youtube] Extracting URL: https://youtube.com/..."})
		progress(job.Progress{Done: 75, LogLine: "[download] 75% of 50MiB at 5MiB/s"})
		close(doneCh)
		return job.Result{Summary: "Done"}, nil
	}))
	p.state.ActiveJobID = jobID

	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for dummy job")
	}

	tt := ui.NewTester(p.View, 1180, 1000)
	tt.Frame()

	if !tt.HasText("Console Output") {
		t.Errorf("expected 'Console Output' header in job card")
	}
	if !tt.HasText("Hide Logs") {
		t.Errorf("expected 'Hide Logs' toggle button")
	}
	if !tt.HasText("[youtube] Extracting URL: https://youtube.com/...") {
		t.Errorf("expected log line visible in console box")
	}

	// Toggle hide logs
	if err := tt.Click("Hide Logs"); err != nil {
		t.Fatalf("Click 'Hide Logs' failed: %v", err)
	}
	tt.Frame()

	if !tt.HasText("Show Logs") {
		t.Errorf("expected 'Show Logs' button after toggling")
	}
}

func TestWorkspacePreviewCardAndDismiss(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mtools-youtube-view-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dummyBin := filepath.Join(tempDir, "bin", BinaryName())
	_ = os.MkdirAll(filepath.Dir(dummyBin), 0755)
	_ = os.WriteFile(dummyBin, []byte("#!/bin/sh\n"), 0755)

	settingsFile := filepath.Join(tempDir, "settings.json")
	store, _ := plugin.NewJSONSettingsStore(settingsFile, PluginSettingsSchema())
	store.Set("binary_path", dummyBin)

	q := job.NewQueue(1, nil)
	p := NewPlugin(tempDir, store, q)
	p.state.URLInput = "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
	p.state.PreviewMetadata = &VideoMetadata{
		Title:          "Rick Astley - Never Gonna Give You Up",
		Channel:        "Rick Astley",
		DurationString: "3:32",
		ViewCount:      1500000000,
	}

	tt := ui.NewTester(p.View, 1180, 1000)
	tt.Frame()

	if !tt.HasText("Video Preview") {
		t.Errorf("expected 'Video Preview' title")
	}
	if !tt.HasText("Rick Astley - Never Gonna Give You Up") {
		t.Errorf("expected video title in preview card")
	}
	if !tt.HasText("yt-dlp Command Preview") {
		t.Errorf("expected command preview header")
	}
	if !tt.HasText("Preview") {
		t.Errorf("expected Preview button")
	}

	// Click Dismiss
	if err := tt.Click("Dismiss"); err != nil {
		t.Fatalf("Click 'Dismiss' failed: %v", err)
	}
	tt.Frame()

	if tt.HasText("Rick Astley - Never Gonna Give You Up") {
		t.Errorf("expected preview card dismissed")
	}
}

func TestJobProcessCancellation(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mtools-youtube-view-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	sleepScript := filepath.Join(tempDir, "sleep-tool")
	scriptContent := "#!/bin/sh\nsleep 30\n"
	if runtime.GOOS == "windows" {
		sleepScript += ".bat"
		scriptContent = "@echo off\r\nping 127.0.0.1 -n 30 > nul\r\n"
	}
	if err := os.WriteFile(sleepScript, []byte(scriptContent), 0755); err != nil {
		t.Fatalf("failed to write sleep script: %v", err)
	}

	q := job.NewQueue(1, nil)
	start := time.Now()
	jobID := q.Enqueue("YouTube Downloader", DownloadJob{
		URL:        "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		BinaryPath: sleepScript,
		OutputDir:  tempDir,
	})

	// Wait briefly for process to start
	time.Sleep(100 * time.Millisecond)

	// Cancel job
	canceled := q.Cancel(jobID)
	if !canceled {
		t.Fatalf("expected job cancellation to succeed")
	}

	// Wait for worker to exit (should be rapid, < 1 second)
	for time.Since(start) < 2*time.Second {
		rec, _ := q.GetRecord(jobID)
		if rec.Status == job.StatusCanceled {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	rec, _ := q.GetRecord(jobID)
	if rec.Status != job.StatusCanceled {
		t.Fatalf("expected job status StatusCanceled, got %v", rec.Status)
	}
}

func TestWorkspaceInvalidURLValidation(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mtools-youtube-view-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create dummy binary so workspace loads form
	dummyBin := filepath.Join(tempDir, "bin", BinaryName())
	_ = os.MkdirAll(filepath.Dir(dummyBin), 0755)
	_ = os.WriteFile(dummyBin, []byte("#!/bin/sh\n"), 0755)

	settingsFile := filepath.Join(tempDir, "settings.json")
	store, _ := plugin.NewJSONSettingsStore(settingsFile, PluginSettingsSchema())
	store.Set("binary_path", dummyBin)

	q := job.NewQueue(1, nil)
	p := NewPlugin(tempDir, store, q)
	p.state.URLInput = "invalid-url"

	tt := ui.NewTester(p.View, 800, 600)
	tt.Frame()

	if !tt.HasText("Download Video") {
		t.Fatalf("expected 'Download Video' button")
	}

	// Click download with invalid URL
	if err := tt.Click("Download Video"); err != nil {
		t.Fatalf("Click failed: %v", err)
	}
	tt.Frame()

	// Should display inline error
	if p.state.InlineError == "" {
		t.Errorf("expected p.state.InlineError to be set")
	}
}

func TestWorkspaceJobCard100PercentProgressBar(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mtools-youtube-view-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dummyBin := filepath.Join(tempDir, "bin", BinaryName())
	_ = os.MkdirAll(filepath.Dir(dummyBin), 0755)
	_ = os.WriteFile(dummyBin, []byte("#!/bin/sh\n"), 0755)

	settingsFile := filepath.Join(tempDir, "settings.json")
	store, _ := plugin.NewJSONSettingsStore(settingsFile, PluginSettingsSchema())
	store.Set("binary_path", dummyBin)

	q := job.NewQueue(1, nil)
	p := NewPlugin(tempDir, store, q)

	jobID := q.Enqueue("YouTube Downloader", job.Func(func(ctx context.Context, progress func(job.Progress)) (job.Result, error) {
		progress(job.Progress{Done: 100, Message: "100.0%", LogLine: "[download] 100% of 50MiB"})
		return job.Result{Summary: "Done"}, nil
	}))
	p.state.ActiveJobID = jobID

	// Wait for job to finish
	time.Sleep(50 * time.Millisecond)

	tt := ui.NewTester(p.View, 800, 600)
	tt.Frame()

	if !tt.HasText("DONE") {
		t.Errorf("expected 'DONE' status badge")
	}
	if !tt.HasText("Download finished.") {
		t.Errorf("expected 'Download finished.' status message")
	}
}

func TestWorkspaceShowInExplorerButton(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mtools-youtube-view-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dummyBin := filepath.Join(tempDir, "bin", BinaryName())
	_ = os.MkdirAll(filepath.Dir(dummyBin), 0755)
	_ = os.WriteFile(dummyBin, []byte("#!/bin/sh\n"), 0755)

	fakeOutput := filepath.Join(tempDir, "sample_video.mp4")
	_ = os.WriteFile(fakeOutput, []byte("fake content"), 0644)

	settingsFile := filepath.Join(tempDir, "settings.json")
	store, _ := plugin.NewJSONSettingsStore(settingsFile, PluginSettingsSchema())
	store.Set("binary_path", dummyBin)

	q := job.NewQueue(1, nil)
	p := NewPlugin(tempDir, store, q)

	jobID := q.Enqueue("YouTube Downloader", job.Func(func(ctx context.Context, progress func(job.Progress)) (job.Result, error) {
		progress(job.Progress{Done: 100, Message: "Done"})
		return job.Result{Outputs: []string{fakeOutput}, Summary: "Done"}, nil
	}))
	p.state.ActiveJobID = jobID

	time.Sleep(50 * time.Millisecond)

	tt := ui.NewTester(p.View, 800, 800)
	tt.Frame()

	if !tt.HasText("Show in Explorer") {
		t.Errorf("expected 'Show in Explorer' button when job finishes successfully")
	}

	// Click button (should not crash)
	if err := tt.Click("Show in Explorer"); err != nil {
		t.Fatalf("Click 'Show in Explorer' failed: %v", err)
	}
}

func TestWorkspaceHistoryPanelResponsive(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mtools-youtube-view-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dummyBin := filepath.Join(tempDir, "bin", BinaryName())
	_ = os.MkdirAll(filepath.Dir(dummyBin), 0755)
	_ = os.WriteFile(dummyBin, []byte("#!/bin/sh\n"), 0755)

	settingsFile := filepath.Join(tempDir, "settings.json")
	store, _ := plugin.NewJSONSettingsStore(settingsFile, PluginSettingsSchema())
	store.Set("binary_path", dummyBin)

	q := job.NewQueue(1, nil)
	p := NewPlugin(tempDir, store, q)

	_ = p.History().Add(HistoryItem{
		ID:           "test-1",
		Title:        "Awesome Song",
		Channel:      "Music Channel",
		Duration:     "4:12",
		FilePath:     "/tmp/song.mp4",
		DownloadedAt: time.Now(),
	})

	// Wide window: history panel visible by default (>=1150px)
	ttWide := ui.NewTester(p.View, 1200, 800)
	ttWide.Frame()
	if !ttWide.HasText("Download History") {
		t.Errorf("expected History Panel to be visible on wide window (>=1150px)")
	}
	if !ttWide.HasText("Awesome Song") {
		t.Errorf("expected history item title 'Awesome Song' in panel")
	}
	if !ttWide.HasText("Clear All") {
		t.Errorf("expected 'Clear All' button in history panel")
	}

	// Click Clear All
	if err := ttWide.Click("Clear All"); err != nil {
		t.Fatalf("Click 'Clear All' failed: %v", err)
	}
	ttWide.Frame()

	if !ttWide.HasText("No downloads yet") {
		t.Errorf("expected empty state after Clear All")
	}
	if len(p.History().All()) != 0 {
		t.Errorf("expected 0 history items remaining, got %d", len(p.History().All()))
	}

	// Narrow window: the inspector is not allowed, whatever the user intent.
	ttNarrow := ui.NewTester(p.View, 800, 600)
	ttNarrow.Frame()
	if ttNarrow.HasText("Download History") {
		t.Errorf("expected History Panel to be hidden on narrow window (<1050px)")
	}
}

func TestWorkspaceHistoryResizable(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mtools-youtube-view-resize-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dummyBin := filepath.Join(tempDir, "bin", BinaryName())
	_ = os.MkdirAll(filepath.Dir(dummyBin), 0755)
	_ = os.WriteFile(dummyBin, []byte("#!/bin/sh\n"), 0755)

	settingsFile := filepath.Join(tempDir, "settings.json")
	store, _ := plugin.NewJSONSettingsStore(settingsFile, PluginSettingsSchema())
	store.Set("binary_path", dummyBin)

	q := job.NewQueue(1, nil)
	p := NewPlugin(tempDir, store, q)

	tt := ui.NewTester(p.View, 1200, 800)
	tt.Frame()

	rect, found := tt.Find("Resize History Panel")
	if !found {
		t.Fatalf("expected 'Resize History Panel' handle on wide viewport")
	}

	// Drag the divider left by 40 DIP: the inspector widens to 360.
	tt.Press(rect.X+rect.W/2, rect.Y+rect.H/2)
	tt.Move(rect.X+rect.W/2-40, rect.Y+rect.H/2)
	tt.Frame()
	tt.Release(rect.X+rect.W/2-40, rect.Y+rect.H/2)
	tt.Frame()

	if p.state.HistoryWidth != 360 {
		t.Fatalf("expected HistoryWidth 360 after drag, got %d", p.state.HistoryWidth)
	}

	// Double click the divider to reset to the default 320.
	rect, _ = tt.Find("Resize History Panel")
	tt.ClickAt(rect.X+rect.W/2, rect.Y+rect.H/2)
	tt.ClickAt(rect.X+rect.W/2, rect.Y+rect.H/2)
	tt.Frame()

	if p.state.HistoryWidth != 320 {
		t.Fatalf("expected HistoryWidth reset to 320, got %d", p.state.HistoryWidth)
	}
}

func TestWorkspaceHistoryMinimizing(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mtools-youtube-view-hist-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dummyBin := filepath.Join(tempDir, "bin", BinaryName())
	_ = os.MkdirAll(filepath.Dir(dummyBin), 0755)
	_ = os.WriteFile(dummyBin, []byte("#!/bin/sh\n"), 0755)

	settingsFile := filepath.Join(tempDir, "settings.json")
	store, _ := plugin.NewJSONSettingsStore(settingsFile, PluginSettingsSchema())
	store.Set("binary_path", dummyBin)

	q := job.NewQueue(1, nil)
	p := NewPlugin(tempDir, store, q)

	// Wide viewport: history open by default
	tt := ui.NewTester(p.View, 1200, 800)
	tt.Frame()

	if !tt.HasText("Download History") {
		t.Errorf("expected 'Download History' visible on wide viewport")
	}
	if tt.HasText("Hide »") {
		t.Errorf("expected no 'Hide »' button inside history panel")
	}

	// Collapse via "Hide History" header button
	if err := tt.Click("Hide History"); err != nil {
		t.Fatalf("Click 'Hide History' failed: %v", err)
	}
	tt.Frame()

	if tt.HasText("Download History") {
		t.Errorf("expected 'Download History' hidden after clicking 'Hide History'")
	}
	if !tt.HasText("History (0)") {
		t.Errorf("expected 'History (0)' expand button in workspace header")
	}

	// Expand via "History (0)"
	if err := tt.Click("History (0)"); err != nil {
		t.Fatalf("Click 'History (0)' failed: %v", err)
	}
	tt.Frame()

	if !tt.HasText("Download History") {
		t.Errorf("expected 'Download History' restored after expand")
	}

	// Collapse via header "Hide History" again
	if err := tt.Click("Hide History"); err != nil {
		t.Fatalf("Click 'Hide History' failed: %v", err)
	}
	tt.Frame()

	if tt.HasText("Download History") {
		t.Errorf("expected 'Download History' hidden after clicking 'Hide History'")
	}
}

func TestWorkspaceMissingFileToast(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mtools-youtube-view-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dummyBin := filepath.Join(tempDir, "bin", BinaryName())
	_ = os.MkdirAll(filepath.Dir(dummyBin), 0755)
	_ = os.WriteFile(dummyBin, []byte("#!/bin/sh\n"), 0755)

	settingsFile := filepath.Join(tempDir, "settings.json")
	store, _ := plugin.NewJSONSettingsStore(settingsFile, PluginSettingsSchema())
	store.Set("binary_path", dummyBin)

	q := job.NewQueue(1, nil)
	p := NewPlugin(tempDir, store, q)

	nonExistentFile := filepath.Join(tempDir, "deleted_video.mp4")
	_ = p.History().Add(HistoryItem{
		ID:           "deleted-1",
		Title:        "Deleted Video",
		Channel:      "Test Channel",
		Duration:     "1:23",
		FilePath:     nonExistentFile,
		DownloadedAt: time.Now(),
	})

	tt := ui.NewTester(p.View, 1200, 800)
	tt.Frame()

	if !tt.HasText("Deleted Video") {
		t.Fatalf("expected 'Deleted Video' in history panel")
	}

	// Click "Show" for the missing file
	if err := tt.Click("Show"); err != nil {
		t.Fatalf("Click 'Show' failed: %v", err)
	}
	tt.Frame()

	// Assert toast message appears
	if !tt.HasText("File was moved or deleted") {
		t.Errorf("expected 'File was moved or deleted' toast message")
	}

	// Dismiss toast by clicking '✕'
	if err := tt.Click("✕"); err != nil {
		t.Fatalf("Click dismiss toast failed: %v", err)
	}
	tt.Frame()

	if tt.HasText("File was moved or deleted") {
		t.Errorf("expected toast to be dismissed")
	}

	// In narrow window without history sidebar, verify toast is NOT shown in main content
	p.state.ToastMessage = "File was moved or deleted"
	ttNarrow := ui.NewTester(p.View, 800, 800)
	ttNarrow.Frame()
	if ttNarrow.HasText("File was moved or deleted") {
		t.Errorf("expected toast to NOT be shown in main workspace content")
	}
}

func TestWorkspaceCustomFilenameField(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mtools-youtube-view-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dummyBin := filepath.Join(tempDir, "bin", BinaryName())
	_ = os.MkdirAll(filepath.Dir(dummyBin), 0755)
	_ = os.WriteFile(dummyBin, []byte("#!/bin/sh\n"), 0755)

	settingsFile := filepath.Join(tempDir, "settings.json")
	store, _ := plugin.NewJSONSettingsStore(settingsFile, PluginSettingsSchema())
	store.Set("binary_path", dummyBin)

	q := job.NewQueue(1, nil)
	p := NewPlugin(tempDir, store, q)
	p.state.CustomFilename = "my_custom_episode"

	tt := ui.NewTester(p.View, 800, 800)
	tt.Frame()

	if !tt.HasText("Output Filename") {
		t.Errorf("expected 'Output Filename' label in workspace")
	}
	if !tt.HasText("-o \"my_custom_episode.%(ext)s\"") {
		t.Errorf("expected yt-dlp command preview to reflect custom filename flag")
	}
}

func TestWorkspaceAdvancedTabsAndValidation(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mtools-youtube-view-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dummyBin := filepath.Join(tempDir, "bin", BinaryName())
	_ = os.MkdirAll(filepath.Dir(dummyBin), 0755)
	_ = os.WriteFile(dummyBin, []byte("#!/bin/sh\n"), 0755)

	settingsFile := filepath.Join(tempDir, "settings.json")
	store, _ := plugin.NewJSONSettingsStore(settingsFile, PluginSettingsSchema())
	store.Set("binary_path", dummyBin)

	q := job.NewQueue(1, nil)
	p := NewPlugin(tempDir, store, q)
	p.state.URLInput = "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
	p.state.TimeRangeStart = "00:05:00"
	p.state.TimeRangeEnd = "00:01:00"

	tt := ui.NewTester(p.View, 1000, 800)
	tt.Frame()

	// Switch to Trim & Post-Process tab
	if err := tt.Click("Trim & Post-Process"); err != nil {
		t.Fatalf("Click 'Trim & Post-Process' failed: %v", err)
	}
	tt.Frame()
	if !tt.HasText("Time-Range Clipping") || !tt.HasText("SponsorBlock") {
		t.Errorf("expected Trim & Post-Process controls to render")
	}

	// Trigger Download Video to verify time-range validation
	if err := tt.Click("Download Video"); err != nil {
		t.Fatalf("Click 'Download Video' failed: %v", err)
	}
	tt.Frame()
	if p.state.TimeRangeError == "" {
		t.Errorf("expected TimeRangeError for start >= end")
	}

	// Switch to Network & Auth tab
	if err := tt.Click("Network & Auth"); err != nil {
		t.Fatalf("Click 'Network & Auth' failed: %v", err)
	}
	tt.Frame()
	if !tt.HasText("Browser Cookies") || !tt.HasText("Concurrent Fragments") {
		t.Errorf("expected Network & Auth controls to render")
	}
}

func TestWorkspacePlaylistCardAndBatchEnqueue(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mtools-youtube-view-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dummyBin := filepath.Join(tempDir, "bin", BinaryName())
	_ = os.MkdirAll(filepath.Dir(dummyBin), 0755)
	_ = os.WriteFile(dummyBin, []byte("#!/bin/sh\n"), 0755)

	settingsFile := filepath.Join(tempDir, "settings.json")
	store, _ := plugin.NewJSONSettingsStore(settingsFile, PluginSettingsSchema())
	store.Set("binary_path", dummyBin)

	q := job.NewQueue(1, nil)
	p := NewPlugin(tempDir, store, q)
	p.state.PlaylistMetadata = &PlaylistMetadata{
		Title: "My Playlist",
		Entries: []PlaylistEntry{
			{Index: 1, Title: "Episode 1", URL: "https://www.youtube.com/watch?v=ep1", DurationString: "1:00"},
			{Index: 2, Title: "Episode 2", URL: "https://www.youtube.com/watch?v=ep2", DurationString: "2:00"},
		},
	}
	p.state.SelectedEntries["https://www.youtube.com/watch?v=ep1"] = true
	p.state.SelectedEntries["https://www.youtube.com/watch?v=ep2"] = true

	tt := ui.NewTester(p.View, 1000, 900)
	tt.Frame()

	if !tt.HasText("My Playlist") || !tt.HasText("Download Selected (2)") {
		t.Fatalf("expected playlist card and 'Download Selected (2)' button")
	}

	if err := tt.Click("Download Selected (2)"); err != nil {
		t.Fatalf("Click 'Download Selected (2)' failed: %v", err)
	}
	tt.Frame()
	if len(p.state.BatchJobIDs) != 2 {
		t.Errorf("expected 2 batch jobs enqueued, got %d", len(p.state.BatchJobIDs))
	}
}

func TestWorkspaceResponsiveNarrowViewports(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mtools-youtube-responsive-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dummyBin := filepath.Join(tempDir, "bin", BinaryName())
	_ = os.MkdirAll(filepath.Dir(dummyBin), 0755)
	_ = os.WriteFile(dummyBin, []byte("#!/bin/sh\n"), 0755)

	settingsFile := filepath.Join(tempDir, "settings.json")
	store, _ := plugin.NewJSONSettingsStore(settingsFile, PluginSettingsSchema())
	store.Set("binary_path", dummyBin)

	q := job.NewQueue(1, nil)
	p := NewPlugin(tempDir, store, q)
	p.state.PreviewMetadata = &VideoMetadata{
		Title:          "Very Long Responsive Test Video Title That Should Wrap Cleanly",
		Channel:        "Test Channel",
		DurationString: "10:30",
	}

	for _, vp := range [][2]int{{484, 520}, {720, 520}, {900, 700}, {1180, 760}} {
		p.state.HistoryInit = true
		p.state.HistoryOpen = vp[0] >= 1150
		tt := ui.NewTester(p.View, vp[0], vp[1])

		paneRight := float32(vp[0])
		if p.state.HistoryOpen && vp[0] >= 1050 {
			paneRight = float32(vp[0] - p.state.HistoryWidth)
		}

		// Tab 0: Stream & Format (including ui.Select trigger text like "Best (VBR 0)")
		p.state.OptionsTab = 0
		tt.Frame()
		for _, want := range []string{"Video Stream", "Audio Stream", "Format", "Audio Quality", "Best (VBR 0)", "mp4", "Output Filename", "Download Video"} {
			if !tt.HasText(want) {
				t.Errorf("viewport %dx%d tab 0 missing %q", vp[0], vp[1], want)
			}
			if r, ok := tt.Find(want); ok && r.X+r.W > paneRight {
				t.Errorf("viewport %dx%d tab 0 %q overflows pane right edge: %.1f > %.1f", vp[0], vp[1], want, r.X+r.W, paneRight)
			}
		}

		// Tab 1: Trim & Post-Process
		p.state.OptionsTab = 1
		p.state.SponsorBlockMode = "remove"
		tt.Frame()
		for _, want := range []string{"Time-Range Clipping", "Keyframe Cuts", "SponsorBlock", "Metadata & Subtitles", "Embed Subtitles"} {
			if !tt.HasText(want) {
				t.Errorf("viewport %dx%d tab 1 missing %q", vp[0], vp[1], want)
			}
			if r, ok := tt.Find(want); ok && r.X+r.W > paneRight {
				t.Errorf("viewport %dx%d tab 1 %q overflows pane right edge: %.1f > %.1f", vp[0], vp[1], want, r.X+r.W, paneRight)
			}
		}

		// Tab 2: Network & Auth
		p.state.OptionsTab = 2
		tt.Frame()
		for _, want := range []string{"Browser Cookies", "Concurrent Fragments", "Proxy URL", "Rate Limit", "Update yt-dlp"} {
			if !tt.HasText(want) {
				t.Errorf("viewport %dx%d tab 2 missing %q", vp[0], vp[1], want)
			}
			if r, ok := tt.Find(want); ok && r.X+r.W > paneRight {
				t.Errorf("viewport %dx%d tab 2 %q overflows pane right edge: %.1f > %.1f", vp[0], vp[1], want, r.X+r.W, paneRight)
			}
		}
	}
}

func TestWorkspacePlaylistPreviewScroll(t *testing.T) {
	tempDir := t.TempDir()
	dummyBin := filepath.Join(tempDir, "bin", BinaryName())
	_ = os.MkdirAll(filepath.Dir(dummyBin), 0755)
	_ = os.WriteFile(dummyBin, []byte("#!/bin/sh\n"), 0755)

	settingsFile := filepath.Join(tempDir, "settings.json")
	store, _ := plugin.NewJSONSettingsStore(settingsFile, PluginSettingsSchema())
	store.Set("binary_path", dummyBin)

	q := job.NewQueue(1, nil)
	p := NewPlugin(tempDir, store, q)

	entries := make([]PlaylistEntry, 20)
	for i := range entries {
		entries[i] = PlaylistEntry{
			Index:          i + 1,
			Title:          fmt.Sprintf("Episode %02d", i+1),
			URL:            fmt.Sprintf("https://www.youtube.com/watch?v=ep%02d", i+1),
			DurationString: "1:00",
		}
	}
	p.state.PlaylistMetadata = &PlaylistMetadata{
		Title:   "Long Playlist",
		Entries: entries,
	}

	tt := ui.NewTester(p.View, 1000, 900)
	tt.Frame()

	if !tt.HasText("Episode 01") {
		t.Fatalf("expected Episode 01 to be visible initially")
	}
	if tt.HasText("Episode 20") {
		t.Fatalf("expected Episode 20 to be virtualized offscreen initially")
	}

	ep1Rect, ok := tt.Find("Episode 01")
	if !ok {
		t.Fatalf("expected to find Episode 01 bounds")
	}

	// Scroll down inside the playlist list viewport.
	scrollX := ep1Rect.X + ep1Rect.W/2
	scrollY := ep1Rect.Y + ep1Rect.H/2
	for i := 0; i < 6; i++ {
		tt.Scroll(scrollX, scrollY, 0, 200)
		tt.Frame()
	}

	if !tt.HasText("Episode 20") {
		t.Fatalf("expected Episode 20 to become visible after scrolling down")
	}
	if tt.HasText("Episode 01") {
		t.Fatalf("expected Episode 01 to scroll out of view after scrolling down")
	}

	if err := tt.Click("Dismiss"); err != nil {
		t.Fatalf("Click Dismiss failed: %v", err)
	}
	tt.Frame()
	if p.state.PlaylistMetadata != nil {
		t.Fatalf("expected PlaylistMetadata cleared after Dismiss")
	}
}

func TestWorkspacePlaylistBatchShowsFirstJobLogs(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mtools-youtube-batch-logs-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dummyBin := filepath.Join(tempDir, "bin", BinaryName())
	_ = os.MkdirAll(filepath.Dir(dummyBin), 0755)
	_ = os.WriteFile(dummyBin, []byte("#!/bin/sh\n"), 0755)

	settingsFile := filepath.Join(tempDir, "settings.json")
	store, _ := plugin.NewJSONSettingsStore(settingsFile, PluginSettingsSchema())
	store.Set("binary_path", dummyBin)

	q := job.NewQueue(1, nil)
	p := NewPlugin(tempDir, store, q)

	startedFirst := make(chan struct{})
	releaseFirst := make(chan struct{})

	id1 := q.Enqueue("Item 1", job.Func(func(ctx context.Context, progress func(job.Progress)) (job.Result, error) {
		progress(job.Progress{Done: 25, Total: 100, Unit: "%", Message: "Downloading item 1", LogLine: "[cmd] yt-dlp item1"})
		close(startedFirst)
		select {
		case <-releaseFirst:
		case <-ctx.Done():
			return job.Result{}, ctx.Err()
		}
		return job.Result{Summary: "Item 1 done"}, nil
	}))
	id2 := q.Enqueue("Item 2", job.Func(func(ctx context.Context, progress func(job.Progress)) (job.Result, error) {
		return job.Result{Summary: "Item 2 done"}, nil
	}))

	// Regression check: even when ActiveJobID is set or BatchJobIDs has 2 items
	// with item #2 still queued, logs from item #1 must be visible immediately.
	p.state.BatchJobIDs = []job.JobID{id1, id2}
	p.state.ActiveJobID = id1

	select {
	case <-startedFirst:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for first batch job to start")
	}

	tt := ui.NewTester(p.View, 1180, 1000)
	tt.Frame()

	if !tt.HasText("Batch Download (0/2)") {
		t.Errorf("expected 'Batch Download (0/2)' header, texts: %v", tt.Texts())
	}
	if !tt.HasText("[#1] [cmd] yt-dlp item1") {
		t.Errorf("expected first item's log line visible while second item is queued, texts: %v", tt.Texts())
	}
	if !tt.HasText("Cancel All") {
		t.Errorf("expected 'Cancel All' button while batch is running")
	}

	close(releaseFirst)
	q.Cancel(id2)
}

func TestWorkspacePreviewLogsAndProgress(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mtools-youtube-preview-logs-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dummyBin := filepath.Join(tempDir, "bin", BinaryName())
	_ = os.MkdirAll(filepath.Dir(dummyBin), 0755)
	_ = os.WriteFile(dummyBin, []byte("#!/bin/sh\n"), 0755)

	settingsFile := filepath.Join(tempDir, "settings.json")
	store, _ := plugin.NewJSONSettingsStore(settingsFile, PluginSettingsSchema())
	store.Set("binary_path", dummyBin)

	q := job.NewQueue(1, nil)
	p := NewPlugin(tempDir, store, q)

	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.state.startPreviewRun(cancel)
	p.state.setPreviewProgress(40, "Fetching video metadata...")
	p.state.appendPreviewLog("[cmd] yt-dlp --dump-single-json --no-playlist https://youtube.com/watch?v=abc")
	p.state.appendPreviewLog("[youtube] Extracting URL: abc")

	tt := ui.NewTester(p.View, 1180, 1000)
	tt.Frame()

	if !tt.HasText("Preview Process") {
		t.Errorf("expected 'Preview Process' card")
	}
	if !tt.HasText("Fetching video metadata...") {
		t.Errorf("expected preview status text")
	}
	if !tt.HasText("Preview Console Output (2 lines)") {
		t.Errorf("expected preview console output header")
	}
	if !tt.HasText("[youtube] Extracting URL: abc") {
		t.Errorf("expected preview CLI log line visible")
	}
	if !tt.HasText("Cancel") {
		t.Errorf("expected Cancel button while preview is loading")
	}
}
