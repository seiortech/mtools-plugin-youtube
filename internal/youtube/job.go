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

	"github.com/seiortech/mtools-sdk/pluginapi"
)

// DownloadJob runs yt-dlp as a subprocess to download media.
type DownloadJob struct {
	CommandOptions
	CustomArgs []string
}

// Run executes yt-dlp and streams progress lines.
func (j DownloadJob) Run(ctx context.Context, progress func(pluginapi.Progress)) (pluginapi.RunResult, error) {
	if j.BinaryPath == "" {
		return pluginapi.RunResult{}, ErrMissingBinary
	}

	sanitizedURL, err := ParseURL(j.URL)
	if err != nil {
		return pluginapi.RunResult{}, err
	}

	if _, err := ParseTimeRange(j.TimeRangeStart, j.TimeRangeEnd); err != nil {
		return pluginapi.RunResult{}, err
	}
	if _, err := ParseRateLimit(j.RateLimit); err != nil {
		return pluginapi.RunResult{}, err
	}
	if _, err := ParseProxyURL(j.ProxyURL); err != nil {
		return pluginapi.RunResult{}, err
	}
	if _, err := ValidateCookiesFile(j.CookiesFile); err != nil {
		return pluginapi.RunResult{}, err
	}

	outDir := expandOutputDir(j.OutputDir)
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return pluginapi.RunResult{}, fmt.Errorf("failed to create destination directory: %w", err)
	}

	cmdOpts := j.CommandOptions
	cmdOpts.URL = sanitizedURL
	cmdOpts.OutputDir = outDir

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
		progress(pluginapi.Progress{
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
		return pluginapi.RunResult{}, fmt.Errorf("failed to open stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return pluginapi.RunResult{}, fmt.Errorf("failed to open stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return pluginapi.RunResult{}, fmt.Errorf("failed to start yt-dlp: %w", err)
	}

	var mu sync.Mutex
	var lastUpdate time.Time
	var lastPct float64
	var lastMsg = "Starting yt-dlp..."
	var finalOutputFile string
	var jobDestinations []string
	var stderrBuf bytes.Buffer

	emitLine := func(line string, isStderr bool) {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			return
		}

		var toEmit *pluginapi.Progress

		mu.Lock()
		if isStderr && stderrBuf.Len() < maxStderrCapture {
			if stderrBuf.Len() > 0 {
				stderrBuf.WriteByte('\n')
			}
			stderrBuf.WriteString(trimmed)
		}

		if out, ok := parseOutputLine(trimmed); ok {
			finalOutputFile = out
			jobDestinations = append(jobDestinations, out)
		}

		if p, ok := ParseProgressLine(line); ok {
			lastPct = p.Done
			if p.Message != "" {
				lastMsg = p.Message
			}
			p.LogLine = line
			// Throttle progress updates to at most once per 60ms (first update emits immediately).
			if progress != nil && (lastUpdate.IsZero() || time.Since(lastUpdate) >= 60*time.Millisecond || p.Done >= 100) {
				lastUpdate = time.Now()
				toEmit = &p
			}
		} else {
			if stageMsg, ok := classifyStageMessage(trimmed); ok {
				lastMsg = stageMsg
			}
			if progress != nil {
				toEmit = &pluginapi.Progress{
					Done:    lastPct,
					Total:   100,
					Unit:    "%",
					Message: lastMsg,
					LogLine: trimmed,
				}
			}
		}
		mu.Unlock()

		if toEmit != nil {
			progress(*toEmit)
		}
	}

	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		errScanner := bufio.NewScanner(stderr)
		errScanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for errScanner.Scan() {
			if ctx.Err() != nil {
				break
			}
			emitLine(errScanner.Text(), true)
		}
		if sErr := errScanner.Err(); sErr != nil && ctx.Err() == nil {
			emitLine(fmt.Sprintf("[stderr-scan-error] %v", sErr), true)
		}
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		if ctx.Err() != nil {
			break
		}
		emitLine(scanner.Text(), false)
	}
	if sErr := scanner.Err(); sErr != nil && ctx.Err() == nil {
		emitLine(fmt.Sprintf("[stdout-scan-error] %v", sErr), true)
	}

	<-stderrDone
	waitErr := cmd.Wait()
	if waitErr != nil {
		if ctx.Err() != nil || errors.Is(waitErr, context.Canceled) {
			cleanJobPartialFiles(outDir, jobDestinations)
			return pluginapi.RunResult{}, context.Canceled
		}
		errMsg := ExtractLastError(stderrBuf.String())
		if errMsg == "" {
			errMsg = waitErr.Error()
		}
		return pluginapi.RunResult{}, fmt.Errorf("yt-dlp failed: %s", errMsg)
	}

	absDir, _ := filepath.Abs(outDir)
	outputPath := resolveFinalOutputPath(absDir, finalOutputFile, j.OutputFormat)
	return pluginapi.RunResult{
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
	if s, ok := strings.CutPrefix(trimmed, "[VideoRemuxer] Remuxing video from "); ok {
		if idx := strings.LastIndex(s, " to "); idx >= 0 {
			return strings.Trim(s[idx+4:], `"'`), true
		}
	}
	if s, ok := strings.CutPrefix(trimmed, "[MoveFiles] Moving file "); ok {
		if idx := strings.LastIndex(s, " to "); idx >= 0 {
			return strings.Trim(s[idx+4:], `"'`), true
		}
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

	for _, ext := range []string{"mp4", "mkv", "webm", "m4a", "mp3", "opus", "flac", "wav"} {
		testPath := filepath.Join(dir, cleanedBase+"."+ext)
		if fi, err := os.Stat(testPath); err == nil && !fi.IsDir() {
			return testPath
		}
	}

	return candidate
}

// cleanJobPartialFiles removes only .part, .ytdl, and .part-Frag* files belonging
// to destinations tracked by this specific job, never touching unrelated files in outDir.
func cleanJobPartialFiles(outDir string, destinations []string) {
	if len(destinations) == 0 {
		return
	}
	absDir, err := filepath.Abs(outDir)
	if err != nil {
		absDir = outDir
	}
	seen := make(map[string]struct{}, len(destinations))
	for _, dest := range destinations {
		if dest == "" {
			continue
		}
		var basePath string
		if filepath.IsAbs(dest) {
			basePath = dest
		} else {
			basePath = filepath.Join(absDir, filepath.Base(dest))
		}
		if _, ok := seen[basePath]; ok {
			continue
		}
		seen[basePath] = struct{}{}

		_ = os.Remove(basePath + ".part")
		_ = os.Remove(basePath + ".ytdl")
		if frags, err := filepath.Glob(basePath + ".part-Frag*"); err == nil {
			for _, f := range frags {
				_ = os.Remove(f)
			}
		}
	}
}

func stringsTrim(s string) string {
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}
