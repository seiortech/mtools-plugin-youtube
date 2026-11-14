package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/seiortech/mtools-plugin-youtube/internal/youtube"
	"github.com/seiortech/mtools-sdk/pluginapi"
)

type ytServer struct{}

func resolveYTBinary(settings map[string]string) (string, error) {
	bin := settings["binary_path"]
	if bin == "" {
		bin = settings["helper_yt-dlp"]
	}
	return youtube.ResolveBinary(bin, settings["plugin_data_dir"])
}

func resolveFFmpegBinary(settings map[string]string) (string, error) {
	bin := settings["ffmpeg_path"]
	if bin == "" {
		bin = settings["helper_ffmpeg"]
	}
	return youtube.ResolveFFmpeg(bin, settings["plugin_data_dir"])
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

func commandOptionsFromParams(p pluginapi.RunParams, bin, ffmpegBin, validURL, outDir string) youtube.CommandOptions {
	outputFormat := firstInput(p.Inputs, "output_format")
	if outputFormat == "" {
		outputFormat = firstInput(p.Inputs, "format")
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
	cookiesBrowser := firstInput(p.Inputs, "cookies_browser")
	if cookiesBrowser == "" {
		cookiesBrowser = p.Settings["cookies_from_browser"]
	}
	if cookiesBrowser == "none" {
		cookiesBrowser = ""
	}
	proxyURL := firstInput(p.Inputs, "proxy_url")
	if proxyURL == "" {
		proxyURL = p.Settings["proxy_url"]
	}
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
		VideoStream:         firstInput(p.Inputs, "video_stream"),
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
		CookiesFromBrowser:  cookiesBrowser,
		CookiesFile:         firstInput(p.Inputs, "cookies_file"),
		ProxyURL:            proxyURL,
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

	bin, err := resolveYTBinary(p.Settings)
	res.HelperStatus["yt-dlp"] = pluginapi.HelperStatus{
		Found:   err == nil,
		Needed:  true,
		Path:    bin,
		Message: "yt-dlp binary is required to download videos",
	}

	ffmpegBin, ffmpegErr := resolveFFmpegBinary(p.Settings)

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

		if youtube.IsPlaylistURL(validURL) {
			appendLog(fmt.Sprintf("[cmd] %s --flat-playlist --dump-single-json %s", bin, validURL))
			pm, pmErr := youtube.FetchPlaylistMetadataWithLogs(ctx, bin, validURL, appendLog)
			if pmErr != nil {
				res.Error = pmErr.Error()
				return res, nil
			}
			appendLog(fmt.Sprintf("Playlist loaded: %s (%d items)", pm.Title, len(pm.Entries)))
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
			appendLog(fmt.Sprintf("[cmd] %s --dump-single-json --no-playlist %s", bin, validURL))
			meta, metaErr := youtube.FetchMetadataWithLogs(ctx, bin, validURL, appendLog)
			if metaErr != nil {
				res.Error = metaErr.Error()
				return res, nil
			}
			appendLog(fmt.Sprintf("Metadata ready: %s", meta.Title))
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
		return runBinaryDownload(ctx, p, youtube.BinaryName(), "yt-dlp", "Connecting to GitHub releases...", youtube.DownloadBinaryTo, emit)

	case "fetch_ffmpeg":
		return runBinaryDownload(ctx, p, youtube.FFmpegBinaryName(), "ffmpeg", "Connecting to ffmpeg release...", youtube.DownloadFFmpegTo, emit)

	case "update_ytdlp":
		bin, err := resolveYTBinary(p.Settings)
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

		bin, err := resolveYTBinary(p.Settings)
		if err != nil {
			return pluginapi.RunResult{}, err
		}
		ffmpegBin, _ := resolveFFmpegBinary(p.Settings)

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

		var allOutputs []string
		var summaries []string
		for idx, targetURL := range targets {
			downloadJob := youtube.DownloadJob{
				URL:                 targetURL,
				BinaryPath:          bin,
				FFmpegPath:          ffmpegBin,
				OutputDir:           outDir,
				VideoStream:         opts.VideoStream,
				AudioStream:         opts.AudioStream,
				OutputFormat:        opts.OutputFormat,
				AudioQuality:        opts.AudioQuality,
				EmbedSubs:           opts.EmbedSubs,
				SubLangs:            opts.SubLangs,
				WriteAutoSubs:       opts.WriteAutoSubs,
				EmbedThumbnail:      opts.EmbedThumbnail,
				EmbedChapters:       opts.EmbedChapters,
				EmbedMetadata:       opts.EmbedMetadata,
				SponsorBlockMode:    opts.SponsorBlockMode,
				SponsorBlockCats:    opts.SponsorBlockCats,
				TimeRangeStart:      opts.TimeRangeStart,
				TimeRangeEnd:        opts.TimeRangeEnd,
				ForceKeyframesCuts:  opts.ForceKeyframesCuts,
				CookiesFromBrowser:  opts.CookiesFromBrowser,
				CookiesFile:         opts.CookiesFile,
				ProxyURL:            opts.ProxyURL,
				RateLimit:           opts.RateLimit,
				ConcurrentFragments: opts.ConcurrentFragments,
				CustomFilename:      opts.CustomFilename,
				IsPlaylistItem:      isPlaylist || opts.IsPlaylistItem,
			}

			res, err := downloadJob.Run(ctx, func(pr pluginapi.Progress) {
				msg := pr.Message
				if len(targets) > 1 {
					msg = fmt.Sprintf("[%d/%d] %s", idx+1, len(targets), pr.Message)
				}
				_ = emit(pluginapi.Progress{
					JobID:   p.JobID,
					Done:    pr.Done,
					Total:   pr.Total,
					Unit:    pr.Unit,
					Message: msg,
					LogLine: pr.LogLine,
				})
			})
			if err != nil {
				return pluginapi.RunResult{}, err
			}
			allOutputs = append(allOutputs, res.Outputs...)
			if res.Summary != "" {
				summaries = append(summaries, res.Summary)
			}
		}

		return pluginapi.RunResult{
			JobID:   p.JobID,
			Outputs: allOutputs,
			Summary: strings.Join(summaries, "\n"),
		}, nil

	default:
		return pluginapi.RunResult{}, errors.New("unknown action")
	}
}

func runBinaryDownload(
	ctx context.Context,
	p pluginapi.RunParams,
	binName, displayName, connectLog string,
	downloadFn func(context.Context, string, func(pluginapi.Progress)) (string, error),
	emit func(pluginapi.Progress) error,
) (pluginapi.RunResult, error) {
	outDirs := p.Inputs["output_dir"]
	if len(outDirs) == 0 || outDirs[0] == "" {
		return pluginapi.RunResult{}, errors.New("missing destination directory")
	}
	target := filepath.Join(outDirs[0], binName)
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
	server := &ytServer{}
	if err := pluginapi.Serve(context.Background(), os.Stdin, os.Stdout, identity, server); err != nil {
		os.Exit(1)
	}
}
