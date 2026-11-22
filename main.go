package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/seiortech/mtools-plugin-youtube/internal/youtube"
	"github.com/seiortech/mtools-sdk/pluginapi"
)

const (
	binaryCacheTTL   = 5 * time.Second
	metadataCacheTTL = 5 * time.Minute
	maxMetadataCache = 32
)

type cachedBinary struct {
	key       string
	path      string
	err       error
	expiresAt time.Time
}

type cachedMetadataEntry struct {
	video     *youtube.VideoMetadata
	playlist  *youtube.PlaylistMetadata
	expiresAt time.Time
}

type ytServer struct {
	mu          sync.Mutex
	ytBinCache  cachedBinary
	ffBinCache  cachedBinary
	metaCache   map[string]cachedMetadataEntry
	metaOrder   []string
}

func newYTServer() *ytServer {
	return &ytServer{
		metaCache: make(map[string]cachedMetadataEntry),
	}
}

func (s *ytServer) invalidateBinaryCache() {
	s.mu.Lock()
	s.ytBinCache = cachedBinary{}
	s.ffBinCache = cachedBinary{}
	s.mu.Unlock()
}

func (s *ytServer) resolveYTBinary(settings map[string]string) (string, error) {
	bin := settings["binary_path"]
	if bin == "" {
		bin = settings["helper_yt-dlp"]
	}
	dataDir := settings["plugin_data_dir"]
	sharedDir := settings[pluginapi.SettingSharedToolsDir]
	key := bin + "\x00" + dataDir + "\x00" + sharedDir

	s.mu.Lock()
	if s.ytBinCache.key == key && time.Now().Before(s.ytBinCache.expiresAt) {
		p, err := s.ytBinCache.path, s.ytBinCache.err
		s.mu.Unlock()
		return p, err
	}
	s.mu.Unlock()

	var p string
	var err error
	if resolved, ok := pluginapi.NewSharedTools(settings).Resolve(bin, pluginapi.SharedToolSpec{
		FileName:     "yt-dlp",
		IsExecutable: true,
		CheckPATH:    true,
	}); ok {
		p = resolved
	} else {
		p, err = youtube.ResolveBinary(bin, dataDir)
	}

	s.mu.Lock()
	s.ytBinCache = cachedBinary{key: key, path: p, err: err, expiresAt: time.Now().Add(binaryCacheTTL)}
	s.mu.Unlock()
	return p, err
}

func (s *ytServer) resolveFFmpegBinary(settings map[string]string) (string, error) {
	bin := settings["ffmpeg_path"]
	if bin == "" {
		bin = settings["helper_ffmpeg"]
	}
	dataDir := settings["plugin_data_dir"]
	sharedDir := settings[pluginapi.SettingSharedToolsDir]
	key := bin + "\x00" + dataDir + "\x00" + sharedDir

	s.mu.Lock()
	if s.ffBinCache.key == key && time.Now().Before(s.ffBinCache.expiresAt) {
		p, err := s.ffBinCache.path, s.ffBinCache.err
		s.mu.Unlock()
		return p, err
	}
	s.mu.Unlock()

	var p string
	var err error
	if resolved, ok := pluginapi.NewSharedTools(settings).Resolve(bin, pluginapi.SharedToolSpec{
		FileName:     "ffmpeg",
		IsExecutable: true,
		CheckPATH:    true,
	}); ok {
		p = resolved
	} else {
		p, err = youtube.ResolveFFmpeg(bin, dataDir)
	}

	s.mu.Lock()
	s.ffBinCache = cachedBinary{key: key, path: p, err: err, expiresAt: time.Now().Add(binaryCacheTTL)}
	s.mu.Unlock()
	return p, err
}

func (s *ytServer) getCachedMetadata(key string) (cachedMetadataEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.metaCache == nil {
		return cachedMetadataEntry{}, false
	}
	entry, ok := s.metaCache[key]
	if !ok || time.Now().After(entry.expiresAt) {
		if ok {
			delete(s.metaCache, key)
		}
		return cachedMetadataEntry{}, false
	}
	return entry, true
}

func (s *ytServer) setCachedMetadata(key string, entry cachedMetadataEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.metaCache == nil {
		s.metaCache = make(map[string]cachedMetadataEntry)
	}
	if _, exists := s.metaCache[key]; !exists {
		if len(s.metaOrder) >= maxMetadataCache {
			evict := s.metaOrder[0]
			s.metaOrder = s.metaOrder[1:]
			delete(s.metaCache, evict)
		}
		s.metaOrder = append(s.metaOrder, key)
	}
	entry.expiresAt = time.Now().Add(metadataCacheTTL)
	s.metaCache[key] = entry
}

func firstInput(inputs map[string][]string, key string) string {
	if vals := inputs[key]; len(vals) > 0 {
		return vals[0]
	}
	return ""
}

func formatViews(views int64) string {
	switch {
	case views >= 1_000_000:
		return fmt.Sprintf("%.1fM views", float64(views)/1_000_000)
	case views >= 1_000:
		return fmt.Sprintf("%.1fK views", float64(views)/1_000)
	case views > 0:
		return fmt.Sprintf("%d views", views)
	default:
		return ""
	}
}

func metadataOptionsFromInputs(inputs map[string][]string, settings map[string]string) youtube.MetadataOptions {
	cookiesBrowser := firstInput(inputs, "cookies_browser")
	if cookiesBrowser == "" {
		cookiesBrowser = settings["cookies_from_browser"]
	}
	if cookiesBrowser == "none" {
		cookiesBrowser = ""
	}
	proxyURL := firstInput(inputs, "proxy_url")
	if proxyURL == "" {
		proxyURL = settings["proxy_url"]
	}
	return youtube.MetadataOptions{
		CookiesFromBrowser: cookiesBrowser,
		CookiesFile:        firstInput(inputs, "cookies_file"),
		ProxyURL:           proxyURL,
	}
}

func commandOptionsFromParams(p pluginapi.RunParams, bin, ffmpegBin, validURL, outDir string) youtube.CommandOptions {
	outputFormat := firstInput(p.Inputs, "output_format")
	if outputFormat == "" {
		outputFormat = firstInput(p.Inputs, "format")
	}
	videoStream := firstInput(p.Inputs, "video_stream")
	if videoStream == "" || videoStream == "bestvideo" {
		switch p.Settings["quality"] {
		case "1080":
			videoStream = "bestvideo[height<=1080]"
		case "720":
			videoStream = "bestvideo[height<=720]"
		case "audio":
			videoStream = "none"
			if outputFormat == "" || outputFormat == "mp4" {
				outputFormat = "mp3"
			}
		}
	}

	filenameTemplate := firstInput(p.Inputs, "filename_template")
	if filenameTemplate == "" {
		filenameTemplate = firstInput(p.Inputs, "custom_filename")
	}
	embedSubs := p.Settings["embed_subtitles"] == "true"
	if v := firstInput(p.Inputs, "embed_subtitles"); v != "" {
		embedSubs = v == "true"
	} else if v := firstInput(p.Inputs, "embed_subs"); v != "" {
		embedSubs = v == "true"
	}
	metaOpts := metadataOptionsFromInputs(p.Inputs, p.Settings)
	rateLimit := firstInput(p.Inputs, "rate_limit")
	if rateLimit == "" {
		rateLimit = p.Settings["rate_limit"]
	}
	fragStr := firstInput(p.Inputs, "concurrent_fragments")
	if fragStr == "" {
		fragStr = p.Settings["concurrent_fragments"]
	}
	frags, _ := strconv.Atoi(fragStr)

	sbMode := firstInput(p.Inputs, "sponsorblock_mode")
	if sbMode == "none" {
		sbMode = ""
	}

	var sbCats []string
	if cats := p.Inputs["sponsorblock_cats"]; len(cats) > 0 {
		for _, raw := range cats {
			for _, c := range strings.Split(raw, ",") {
				if t := strings.TrimSpace(c); t != "" {
					sbCats = append(sbCats, t)
				}
			}
		}
	}

	return youtube.CommandOptions{
		BinaryPath:          bin,
		FFmpegPath:          ffmpegBin,
		URL:                 validURL,
		OutputDir:           outDir,
		VideoStream:         videoStream,
		AudioStream:         firstInput(p.Inputs, "audio_stream"),
		OutputFormat:        outputFormat,
		AudioQuality:        firstInput(p.Inputs, "audio_quality"),
		EmbedSubs:           embedSubs,
		SubLangs:            firstInput(p.Inputs, "sub_langs"),
		WriteAutoSubs:       firstInput(p.Inputs, "write_auto_subs") == "true",
		EmbedThumbnail:      firstInput(p.Inputs, "embed_thumbnail") == "true",
		EmbedChapters:       firstInput(p.Inputs, "embed_chapters") == "true",
		EmbedMetadata:       firstInput(p.Inputs, "embed_metadata") == "true",
		SponsorBlockMode:    sbMode,
		SponsorBlockCats:    sbCats,
		TimeRangeStart:      firstInput(p.Inputs, "time_start"),
		TimeRangeEnd:        firstInput(p.Inputs, "time_end"),
		ForceKeyframesCuts:  firstInput(p.Inputs, "force_keyframes_cuts") == "true",
		CookiesFromBrowser:  metaOpts.CookiesFromBrowser,
		CookiesFile:         metaOpts.CookiesFile,
		ProxyURL:            metaOpts.ProxyURL,
		RateLimit:           rateLimit,
		ConcurrentFragments: frags,
		CustomFilename:      filenameTemplate,
		IsPlaylistItem:      firstInput(p.Inputs, "is_playlist_item") == "true",
	}
}

func (s *ytServer) Inspect(ctx context.Context, p pluginapi.InspectParams) (pluginapi.InspectResult, error) {
	res := pluginapi.InspectResult{
		Fields:       make(map[string]pluginapi.InspectFieldUpdate),
		Metadata:     make(map[string]string),
		HelperStatus: make(map[string]pluginapi.HelperStatus),
	}

	bin, err := s.resolveYTBinary(p.Settings)
	res.HelperStatus["yt-dlp"] = pluginapi.HelperStatus{
		Found:   err == nil,
		Needed:  true,
		Path:    bin,
		Message: "yt-dlp binary is required to download videos",
	}

	ffmpegBin, ffmpegErr := s.resolveFFmpegBinary(p.Settings)

	outDir := p.Settings["download_dir"]
	if outDir == "" {
		outDir = "~/Downloads"
	}
	if d := firstInput(p.Inputs, "output_dir"); d != "" {
		outDir = d
	}

	rawURL := strings.Join(p.Inputs["url"], "")
	validURL, urlErr := youtube.ParseURL(rawURL)
	cmdURL := "<URL>"
	if urlErr == nil {
		cmdURL = validURL
	}
	opts := commandOptionsFromParams(pluginapi.RunParams{Inputs: p.Inputs, Settings: p.Settings}, bin, ffmpegBin, cmdURL, outDir)
	res.CommandPreview = youtube.BuildCommandString(opts)

	res.HelperStatus["ffmpeg"] = pluginapi.HelperStatus{
		Found:   ffmpegErr == nil,
		Needed:  youtube.NeedsFFmpeg(opts),
		Path:    ffmpegBin,
		Message: "ffmpeg not found — required for merging streams, clipping, audio conversion, or embedding.",
	}

	if p.Action == "preview" {
		if urlErr != nil {
			res.Error = urlErr.Error()
			return res, nil
		}
		if err != nil {
			res.Error = err.Error()
			return res, nil
		}

		appendLog := func(line string) {
			if len(res.Logs) < 500 {
				res.Logs = append(res.Logs, line)
			}
		}

		metaOpts := metadataOptionsFromInputs(p.Inputs, p.Settings)
		cacheKey := validURL + "\x00" + metaOpts.CookiesFromBrowser + "\x00" + metaOpts.CookiesFile + "\x00" + metaOpts.ProxyURL

		if youtube.IsPlaylistURL(validURL) {
			var pm *youtube.PlaylistMetadata
			if cached, ok := s.getCachedMetadata("pl:" + cacheKey); ok && cached.playlist != nil {
				pm = cached.playlist
				appendLog(fmt.Sprintf("[cache] Playlist loaded from cache: %s (%d items)", pm.Title, len(pm.Entries)))
			} else {
				appendLog(fmt.Sprintf("[cmd] %s --flat-playlist --dump-single-json -- %s", bin, validURL))
				var pmErr error
				pm, pmErr = youtube.FetchPlaylistMetadataWithOptions(ctx, bin, validURL, metaOpts, appendLog)
				if pmErr != nil {
					res.Error = pmErr.Error()
					return res, nil
				}
				s.setCachedMetadata("pl:"+cacheKey, cachedMetadataEntry{playlist: pm})
				appendLog(fmt.Sprintf("Playlist loaded: %s (%d items)", pm.Title, len(pm.Entries)))
			}
			res.IsPlaylist = true
			res.Preview = &pluginapi.InspectPreview{
				Title:       pm.Title,
				Subtitle:    pm.Channel,
				Meta:        []string{fmt.Sprintf("%d items", len(pm.Entries))},
				Dismissible: true,
			}
			items := make([]pluginapi.SurfaceOption, 0, len(pm.Entries))
			for _, e := range pm.Entries {
				items = append(items, pluginapi.SurfaceOption{
					Label: e.Title,
					Value: e.URL,
					Meta:  e.DurationString,
				})
			}
			res.Fields["playlist_items"] = pluginapi.InspectFieldUpdate{Options: items}
		} else {
			var meta *youtube.VideoMetadata
			if cached, ok := s.getCachedMetadata("vid:" + cacheKey); ok && cached.video != nil {
				meta = cached.video
				appendLog(fmt.Sprintf("[cache] Metadata loaded from cache: %s", meta.Title))
			} else {
				appendLog(fmt.Sprintf("[cmd] %s --dump-single-json --no-playlist -- %s", bin, validURL))
				var metaErr error
				meta, metaErr = youtube.FetchMetadataWithOptions(ctx, bin, validURL, metaOpts, appendLog)
				if metaErr != nil {
					res.Error = metaErr.Error()
					return res, nil
				}
				s.setCachedMetadata("vid:"+cacheKey, cachedMetadataEntry{video: meta})
				appendLog(fmt.Sprintf("Metadata ready: %s", meta.Title))
			}
			var metaParts []string
			if meta.DurationString != "" {
				metaParts = append(metaParts, meta.DurationString)
			}
			if views := formatViews(meta.ViewCount); views != "" {
				metaParts = append(metaParts, views)
			}
			res.Preview = &pluginapi.InspectPreview{
				Title:        meta.Title,
				Subtitle:     meta.Channel,
				Meta:         metaParts,
				ThumbnailURL: meta.ThumbnailURL,
				Dismissible:  true,
			}
			res.Metadata["title"] = meta.Title
			res.Metadata["channel"] = meta.Channel
			res.Metadata["duration"] = meta.DurationString
			vOpts := youtube.ExtractVideoOptions(meta.Formats)
			vSurface := make([]pluginapi.SurfaceOption, 0, len(vOpts))
			for _, o := range vOpts {
				vSurface = append(vSurface, pluginapi.SurfaceOption{Label: o.Label, Value: o.Value})
			}
			aOpts := youtube.ExtractAudioOptions(meta.Formats)
			aSurface := make([]pluginapi.SurfaceOption, 0, len(aOpts))
			for _, o := range aOpts {
				aSurface = append(aSurface, pluginapi.SurfaceOption{Label: o.Label, Value: o.Value})
			}
			if len(vSurface) > 0 {
				res.Fields["video_stream"] = pluginapi.InspectFieldUpdate{Options: vSurface}
			}
			if len(aSurface) > 0 {
				res.Fields["audio_stream"] = pluginapi.InspectFieldUpdate{Options: aSurface}
			}
		}
	}

	return res, nil
}

func (s *ytServer) Run(ctx context.Context, p pluginapi.RunParams, emit func(pluginapi.Progress) error) (pluginapi.RunResult, error) {
	if p.Tool != "youtube" {
		return pluginapi.RunResult{}, errors.New("unsupported tool")
	}

	switch p.Action {
	case "fetch_ytdlp":
		res, err := runBinaryDownload(ctx, p, youtube.BinaryName(), "yt-dlp", "Connecting to GitHub releases...", youtube.DownloadBinaryTo, emit)
		if err == nil {
			s.invalidateBinaryCache()
		}
		return res, err

	case "fetch_ffmpeg":
		res, err := runBinaryDownload(ctx, p, youtube.FFmpegBinaryName(), "ffmpeg", "Connecting to ffmpeg release...", youtube.DownloadFFmpegTo, emit)
		if err == nil {
			s.invalidateBinaryCache()
		}
		return res, err

	case "update_ytdlp":
		bin, err := s.resolveYTBinary(p.Settings)
		if err != nil {
			return pluginapi.RunResult{}, err
		}
		_ = emit(pluginapi.Progress{
			JobID:   p.JobID,
			Done:    20,
			Total:   100,
			Unit:    "%",
			Message: "Checking for yt-dlp updates...",
		})
		out, err := youtube.UpdateBinary(ctx, bin)
		if err != nil {
			return pluginapi.RunResult{}, err
		}
		_ = emit(pluginapi.Progress{
			JobID:   p.JobID,
			Done:    100,
			Total:   100,
			Unit:    "%",
			Message: "Update check complete",
			LogLine: out,
		})
		return pluginapi.RunResult{
			JobID:   p.JobID,
			Summary: out,
		}, nil

	case "download":
		rawURL := strings.Join(p.Inputs["url"], "")
		validURL, err := youtube.ParseURL(rawURL)
		if err != nil {
			return pluginapi.RunResult{}, err
		}

		bin, err := s.resolveYTBinary(p.Settings)
		if err != nil {
			return pluginapi.RunResult{}, err
		}
		ffmpegBin, _ := s.resolveFFmpegBinary(p.Settings)

		outDir := p.Settings["download_dir"]
		if d := firstInput(p.Inputs, "output_dir"); d != "" {
			outDir = d
		}
		if outDir == "" {
			outDir = "~/Downloads"
		}

		opts := commandOptionsFromParams(p, bin, ffmpegBin, validURL, outDir)
		targets := []string{validURL}
		isPlaylist := false
		if items := p.Inputs["playlist_items"]; len(items) > 0 {
			targets = items
			isPlaylist = true
		}

		retryPolicy := youtube.DefaultRetryPolicy()
		if firstInput(p.Inputs, "auto_retry") == "false" {
			retryPolicy.MaxAttempts = 1
		}
		continueOnError := firstInput(p.Inputs, "continue_on_error") != "false"
		workers := 1
		if w, err := strconv.Atoi(firstInput(p.Inputs, "parallel_downloads")); err == nil && w > 1 {
			if w > 3 {
				w = 3
			}
			workers = w
		}

		return runBatchDownloads(ctx, p.JobID, targets, isPlaylist, opts, retryPolicy, continueOnError, workers, emit)

	default:
		return pluginapi.RunResult{}, errors.New("unknown action")
	}
}

func runBatchDownloads(
	ctx context.Context,
	jobID string,
	targets []string,
	isPlaylist bool,
	baseOpts youtube.CommandOptions,
	retryPolicy youtube.RetryPolicy,
	continueOnError bool,
	workers int,
	emit func(pluginapi.Progress) error,
) (pluginapi.RunResult, error) {
	total := len(targets)
	if workers < 1 {
		workers = 1
	}
	if workers > total {
		workers = total
	}

	type itemResult struct {
		outputs []string
		summary string
		err     error
	}

	results := make([]itemResult, total)
	itemPcts := make([]float64, total)

	var mu sync.Mutex
	var emitMu sync.Mutex
	safeEmit := func(pr pluginapi.Progress) {
		emitMu.Lock()
		_ = emit(pr)
		emitMu.Unlock()
	}

	completed := 0
	var firstErr error

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup

	for idx, targetURL := range targets {
		if runCtx.Err() != nil {
			break
		}

		wg.Add(1)
		go func(idx int, targetURL string) {
			defer wg.Done()

			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-runCtx.Done():
				results[idx] = itemResult{err: context.Canceled}
				return
			}

			jobOpts := baseOpts
			jobOpts.URL = targetURL
			jobOpts.IsPlaylistItem = isPlaylist || baseOpts.IsPlaylistItem
			dlJob := youtube.DownloadJob{CommandOptions: jobOpts}

			res, err := dlJob.RunWithRetry(runCtx, retryPolicy, func(pr pluginapi.Progress) {
				mu.Lock()
				if pr.Done > itemPcts[idx] {
					itemPcts[idx] = pr.Done
				}
				var sumPct float64
				for _, pct := range itemPcts {
					sumPct += pct
				}
				overallPct := sumPct / float64(total)
				msg := pr.Message
				logLine := pr.LogLine
				if total > 1 {
					msg = fmt.Sprintf("[%d/%d] %s", idx+1, total, pr.Message)
					if logLine != "" {
						logLine = fmt.Sprintf("[#%d] %s", idx+1, logLine)
					}
				}
				mu.Unlock()

				safeEmit(pluginapi.Progress{
					JobID:   jobID,
					Done:    overallPct,
					Total:   100,
					Unit:    "%",
					Message: msg,
					LogLine: logLine,
				})
			})

			var skipEmit *pluginapi.Progress
			mu.Lock()
			completed++
			itemPcts[idx] = 100
			if err != nil {
				results[idx] = itemResult{err: err}
				if firstErr == nil {
					firstErr = err
				}
				if total > 1 && continueOnError && !errors.Is(err, context.Canceled) {
					skipEmit = &pluginapi.Progress{
						JobID:   jobID,
						Done:    float64(completed) / float64(total) * 100,
						Total:   100,
						Unit:    "%",
						Message: fmt.Sprintf("[%d/%d] Skipped (%v) — continuing...", idx+1, total, err),
						LogLine: fmt.Sprintf("[#%d] ERROR: %v (skipping and continuing batch)", idx+1, err),
					}
				} else if !continueOnError || errors.Is(err, context.Canceled) {
					cancel()
				}
			} else {
				results[idx] = itemResult{outputs: res.Outputs, summary: res.Summary}
			}
			mu.Unlock()

			if skipEmit != nil {
				safeEmit(*skipEmit)
			}
		}(idx, targetURL)
	}

	wg.Wait()

	if ctx.Err() != nil {
		return pluginapi.RunResult{}, context.Canceled
	}

	var allOutputs []string
	var summaries []string
	var failedCount int
	var lastItemErr error
	for _, r := range results {
		if r.err != nil {
			failedCount++
			lastItemErr = r.err
			continue
		}
		allOutputs = append(allOutputs, r.outputs...)
		if r.summary != "" {
			summaries = append(summaries, r.summary)
		}
	}

	if failedCount > 0 && len(allOutputs) == 0 {
		if lastItemErr != nil {
			return pluginapi.RunResult{}, lastItemErr
		}
		return pluginapi.RunResult{}, firstErr
	}

	summary := strings.Join(summaries, "\n")
	if total > 1 && failedCount > 0 {
		summary = fmt.Sprintf("Downloaded %d/%d items (%d failed)\n%s", total-failedCount, total, failedCount, summary)
	}

	return pluginapi.RunResult{
		JobID:   jobID,
		Outputs: allOutputs,
		Summary: summary,
	}, nil
}

func runBinaryDownload(
	ctx context.Context,
	p pluginapi.RunParams,
	binName, displayName, connectLog string,
	downloadFn func(context.Context, string, func(pluginapi.Progress)) (string, error),
	emit func(pluginapi.Progress) error,
) (pluginapi.RunResult, error) {
	outDir := ""
	if outDirs := p.Inputs["output_dir"]; len(outDirs) > 0 && outDirs[0] != "" {
		outDir = outDirs[0]
	} else if sd := p.SharedToolsDir(); sd != "" {
		outDir = sd
	}
	if outDir == "" {
		return pluginapi.RunResult{}, errors.New("missing destination directory")
	}
	target := filepath.Join(outDir, binName)
	_ = emit(pluginapi.Progress{
		JobID:   p.JobID,
		Done:    10,
		Total:   100,
		Unit:    "%",
		Message: fmt.Sprintf("Downloading %s binary...", displayName),
		LogLine: connectLog,
	})
	path, err := downloadFn(ctx, target, func(pr pluginapi.Progress) {
		_ = emit(pluginapi.Progress{
			JobID:   p.JobID,
			Done:    pr.Done,
			Total:   pr.Total,
			Unit:    pr.Unit,
			Message: pr.Message,
			LogLine: pr.Message,
		})
	})
	if err != nil {
		return pluginapi.RunResult{}, err
	}
	_ = emit(pluginapi.Progress{
		JobID:   p.JobID,
		Done:    100,
		Total:   100,
		Unit:    "%",
		Message: "Download complete",
		LogLine: fmt.Sprintf("%s ready at %s", displayName, path),
	})
	return pluginapi.RunResult{
		JobID:   p.JobID,
		Outputs: []string{path},
		Summary: fmt.Sprintf("Downloaded %s to %s", displayName, path),
	}, nil
}

func main() {
	identity := pluginapi.InitializeResult{
		Protocol: pluginapi.Protocol,
		PluginID: "com.mtools.youtube",
		Version:  "1.0.0",
	}
	server := newYTServer()
	if err := pluginapi.Serve(context.Background(), os.Stdin, os.Stdout, identity, server); err != nil {
		os.Exit(1)
	}
}
