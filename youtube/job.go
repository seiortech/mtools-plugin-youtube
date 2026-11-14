package youtube

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/seiortech/mtools-sdk/job"
)

// DownloadJob runs yt-dlp as a subprocess to download media.
type DownloadJob struct {
	URL                 string
	BinaryPath          string
	FFmpegPath          string
	OutputDir           string
	Quality             string
	VideoStream         string
	AudioStream         string
	OutputFormat        string
	AudioQuality        string
	EmbedSubs           bool
	SubLangs            string
	WriteAutoSubs       bool
	EmbedThumbnail      bool
	EmbedChapters       bool
	EmbedMetadata       bool
	SponsorBlockMode    string
	SponsorBlockCats    []string
	TimeRangeStart      string
	TimeRangeEnd        string
	ForceKeyframesCuts  bool
	CookiesFromBrowser  string
	CookiesFile         string
	ProxyURL            string
	RateLimit           string
	ConcurrentFragments int
	CustomFilename      string
	IsPlaylistItem      bool
	CustomArgs          []string
}

// Run executes yt-dlp and streams progress lines.
func (j DownloadJob) Run(ctx context.Context, progress func(job.Progress)) (job.Result, error) {
	if j.BinaryPath == "" {
		return job.Result{}, ErrMissingBinary
	}

	sanitizedURL, err := ParseURL(j.URL)
	if err != nil {
		return job.Result{}, err
	}

	if _, err := ParseTimeRange(j.TimeRangeStart, j.TimeRangeEnd); err != nil {
		return job.Result{}, err
	}
	if _, err := ParseRateLimit(j.RateLimit); err != nil {
		return job.Result{}, err
	}
	if _, err := ParseProxyURL(j.ProxyURL); err != nil {
		return job.Result{}, err
	}
	if _, err := ValidateCookiesFile(j.CookiesFile); err != nil {
		return job.Result{}, err
	}

	outDir := expandOutputDir(j.OutputDir)
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return job.Result{}, fmt.Errorf("failed to create destination directory: %w", err)
	}

	cmdOpts := CommandOptions{
		BinaryPath:          j.BinaryPath,
		FFmpegPath:          j.FFmpegPath,
		URL:                 sanitizedURL,
		OutputDir:           outDir,
		VideoStream:         j.VideoStream,
		AudioStream:         j.AudioStream,
		OutputFormat:        j.OutputFormat,
		AudioQuality:        j.AudioQuality,
		EmbedSubs:           j.EmbedSubs,
		SubLangs:            j.SubLangs,
		WriteAutoSubs:       j.WriteAutoSubs,
		EmbedThumbnail:      j.EmbedThumbnail,
		EmbedChapters:       j.EmbedChapters,
		EmbedMetadata:       j.EmbedMetadata,
		SponsorBlockMode:    j.SponsorBlockMode,
		SponsorBlockCats:    j.SponsorBlockCats,
		TimeRangeStart:      j.TimeRangeStart,
		TimeRangeEnd:        j.TimeRangeEnd,
		ForceKeyframesCuts:  j.ForceKeyframesCuts,
		CookiesFromBrowser:  j.CookiesFromBrowser,
		CookiesFile:         j.CookiesFile,
		ProxyURL:            j.ProxyURL,
		RateLimit:           j.RateLimit,
		ConcurrentFragments: j.ConcurrentFragments,
		CustomFilename:      j.CustomFilename,
		IsPlaylistItem:      j.IsPlaylistItem,
	}

	args := j.CustomArgs
	var cmdPreview string
	if len(args) == 0 {
		args = BuildArgs(cmdOpts)
		cmdPreview = BuildCommandString(cmdOpts)
	} else {
		cmdPreview = quoteArg(j.BinaryPath)
		for _, a := range args {
			cmdPreview += " " + quoteArg(a)
		}
	}

	if progress != nil {
		progress(job.Progress{
			Done:    0,
			Total:   100,
			Unit:    "%",
			Message: "Starting yt-dlp...",
			LogLine: "[cmd] " + cmdPreview,
		})
	}

	cmd := exec.CommandContext(ctx, j.BinaryPath, args...)
	prepareCommand(cmd)
	cmd.WaitDelay = 2 * time.Second

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return job.Result{}, fmt.Errorf("failed to open stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return job.Result{}, fmt.Errorf("failed to open stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return job.Result{}, fmt.Errorf("failed to start yt-dlp: %w", err)
	}

	var mu sync.Mutex
	lastUpdate := time.Now()
	var lastPct float64
	var lastMsg = "Starting yt-dlp..."
	var finalOutputFile string
	var stderrBuf bytes.Buffer

	emitLine := func(line string, isStderr bool) {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			return
		}

		mu.Lock()
		defer mu.Unlock()

		if isStderr && stderrBuf.Len() < maxStderrCapture {
			if stderrBuf.Len() > 0 {
				stderrBuf.WriteByte('\n')
			}
			stderrBuf.WriteString(trimmed)
		}

		if out, ok := parseOutputLine(trimmed); ok {
			finalOutputFile = out
		}

		if p, ok := ParseProgressLine(line); ok {
			lastPct = p.Done
			if p.Message != "" {
				lastMsg = p.Message
			}
			p.LogLine = line
			// Throttle progress updates to at most once per 60ms
			if progress != nil && (time.Since(lastUpdate) >= 60*time.Millisecond || p.Done >= 100) {
				lastUpdate = time.Now()
				progress(p)
			}
			return
		}

		if stageMsg, ok := classifyStageMessage(trimmed); ok {
			lastMsg = stageMsg
		}

		if progress != nil {
			progress(job.Progress{
				Done:    lastPct,
				Total:   100,
				Unit:    "%",
				Message: lastMsg,
				LogLine: trimmed,
			})
		}
	}

	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		errScanner := bufio.NewScanner(stderr)
		for errScanner.Scan() {
			if ctx.Err() != nil {
				break
			}
			emitLine(errScanner.Text(), true)
		}
	}()

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		if ctx.Err() != nil {
			break
		}
		emitLine(scanner.Text(), false)
	}

	<-stderrDone
	waitErr := cmd.Wait()
	if waitErr != nil {
		if ctx.Err() != nil || errors.Is(waitErr, context.Canceled) {
			cleanPartialFiles(outDir)
			return job.Result{}, job.ErrCanceled
		}
		errMsg := stringsTrim(stderrBuf.String())
		if errMsg == "" {
			errMsg = waitErr.Error()
		}
		return job.Result{}, fmt.Errorf("yt-dlp failed: %s", errMsg)
	}

	absDir, _ := filepath.Abs(outDir)
	outputPath := resolveFinalOutputPath(absDir, finalOutputFile, j.OutputFormat)
	return job.Result{
		Outputs: []string{outputPath},
		Summary: fmt.Sprintf("Downloaded %s to %s", sanitizedURL, outputPath),
	}, nil
}

func classifyStageMessage(trimmed string) (string, bool) {
	switch {
	case strings.HasPrefix(trimmed, "[youtube]"):
		return "Extracting video info...", true
	case strings.HasPrefix(trimmed, "[info]"):
		return "Preparing download...", true
	case strings.HasPrefix(trimmed, "[Merger]"), strings.HasPrefix(trimmed, "[FixupM3u8]"):
		return "Merging formats...", true
	case strings.HasPrefix(trimmed, "[ExtractAudio]"):
		return "Extracting audio...", true
	case strings.HasPrefix(trimmed, "[SponsorBlock]"):
		return "Applying SponsorBlock...", true
	case strings.HasPrefix(trimmed, "[EmbedThumbnail]"), strings.HasPrefix(trimmed, "[Metadata]"), strings.HasPrefix(trimmed, "[ModifyChapters]"):
		return "Embedding metadata...", true
	case strings.HasPrefix(trimmed, "[ffmpeg]"):
		return "Post-processing with ffmpeg...", true
	default:
		return "", false
	}
}

func parseOutputLine(trimmed string) (string, bool) {
	if s, ok := strings.CutPrefix(trimmed, "[download] Destination: "); ok {
		return strings.Trim(s, `"'`), true
	}
	if s, ok := strings.CutPrefix(trimmed, "[Merger] Merging formats into "); ok {
		return strings.Trim(s, `"'`), true
	}
	if s, ok := strings.CutPrefix(trimmed, "[FixupM3u8] Merging formats into "); ok {
		return strings.Trim(s, `"'`), true
	}
	if s, ok := strings.CutPrefix(trimmed, "[ExtractAudio] Destination: "); ok {
		return strings.Trim(s, `"'`), true
	}
	if s, ok := strings.CutPrefix(trimmed, "[download] "); ok {
		if existing, already := strings.CutSuffix(s, " has already been downloaded"); already {
			return strings.Trim(existing, `"'`), true
		}
	}
	return "", false
}

func expandOutputDir(dir string) string {
	if dir == "" || dir == "~/Downloads" {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "Downloads")
		}
		return "."
	}
	if strings.HasPrefix(dir, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, dir[2:])
		}
	}
	return dir
}

func resolveFinalOutputPath(absDir, finalOutputFile, outputFormat string) string {
	if finalOutputFile == "" {
		return absDir
	}

	var candidate string
	if filepath.IsAbs(finalOutputFile) {
		candidate = finalOutputFile
	} else {
		candidate = filepath.Join(absDir, filepath.Base(finalOutputFile))
	}

	if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() {
		return candidate
	}

	dir := filepath.Dir(candidate)
	base := filepath.Base(candidate)

	cleanedBase := base
	for {
		ext := filepath.Ext(cleanedBase)
		if ext == "" {
			break
		}
		cleanedBase = strings.TrimSuffix(cleanedBase, ext)
		if !strings.HasPrefix(ext, ".f") && ext != ".webm" && ext != ".mp4" && ext != ".m4a" && ext != ".part" {
			break
		}
	}

	if outputFormat != "" {
		formattedCandidate := filepath.Join(dir, cleanedBase+"."+outputFormat)
		if fi, err := os.Stat(formattedCandidate); err == nil && !fi.IsDir() {
			return formattedCandidate
		}
	}

	for _, ext := range []string{"mp4", "mkv", "webm", "m4a", "mp3", "opus", "wav"} {
		testPath := filepath.Join(dir, cleanedBase+"."+ext)
		if fi, err := os.Stat(testPath); err == nil && !fi.IsDir() {
			return testPath
		}
	}

	return candidate
}

func cleanPartialFiles(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".part") || strings.HasSuffix(name, ".ytdl") {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}

func stringsTrim(s string) string {
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}
