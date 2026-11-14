package youtube

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/seiortech/mtools-sdk/job"
	"github.com/seiortech/mtools-sdk/ui/components"
	"github.com/seiortech/mtools-sdk/ui/theme"
)

// ViewState keeps transient UI input states across render frames.
type ViewState struct {
	URLInput            string
	CustomFilename      string
	ToastMessage        string
	ToastTimer          *time.Timer
	InlineError         string
	PreviewError        string
	ActionMessage       string
	ActiveJobID         job.JobID
	SelectedQuality     string
	SelectedVideo       string
	SelectedAudio       string
	SelectedContainer   string
	IsDownloadingBin    bool
	BinDownloadProgress float64
	BinDownloadMsg      string
	ShowLogs            bool

	// Preview state
	previewMu        sync.Mutex
	IsLoadingPreview bool
	PreviewProgress  float64
	PreviewStatus    string
	PreviewFailed    bool
	PreviewCanceled  bool
	PreviewLogs      []string
	ShowPreviewLogs  bool
	previewCancel    context.CancelFunc
	PreviewMetadata  *VideoMetadata
	PreviewThumbnail ui.ImageSource

	// Cached stream format options
	VideoOptions     []FormatOption
	AudioOptions     []FormatOption
	ContainerOptions []FormatOption

	// Advanced options tabs & settings
	OptionsTab           int
	SelectedAudioQuality string
	TimeRangeStart       string
	TimeRangeEnd         string
	ForceKeyframesCuts   bool
	TimeRangeError       string
	SponsorBlockMode     string
	SponsorBlockCats     map[string]bool
	EmbedSubs            bool
	SubLangs             string
	WriteAutoSubs        bool
	EmbedThumbnail       bool
	EmbedChapters        bool
	EmbedMetadata        bool
	CookiesFromBrowser   string
	CookiesFile          string
	ProxyURL             string
	RateLimit            string
	ConcurrentFragments  string
	NetworkError         string
	IsUpdatingBin        bool

	// Playlist preview & batch selection
	PlaylistMetadata *PlaylistMetadata
	SelectedEntries  map[string]bool
	BatchJobIDs      []job.JobID
	PlaylistList     ui.ListState

	// Track recorded completed jobs
	RecordedJobIDs map[job.JobID]bool

	// History panel visibility & size
	HistoryOpen  bool
	HistoryInit  bool
	HistoryWidth int
}

// NewViewState initializes default workspace view state.
func NewViewState() *ViewState {
	return &ViewState{
		SelectedQuality:      "1080",
		SelectedVideo:        "bestvideo",
		SelectedAudio:        "bestaudio",
		SelectedContainer:    "mp4",
		SelectedAudioQuality: "0",
		SponsorBlockCats: map[string]bool{
			"sponsor": true,
		},
		CookiesFromBrowser:  "none",
		ConcurrentFragments: "1",
		ShowLogs:            true,
		ShowPreviewLogs:     true,
		VideoOptions:        DefaultVideoOptions(),
		AudioOptions:        DefaultAudioOptions(),
		ContainerOptions:    DefaultContainerOptions(),
		SelectedEntries:     make(map[string]bool),
		RecordedJobIDs:      make(map[job.JobID]bool),
		HistoryWidth:        320,
	}
}

// RenderWorkspace draws the YouTube Downloader workspace surface.
func (p *Plugin) RenderWorkspace(c *ui.Context) {
	redraw := c.Invalidate

	w, _ := c.Size()
	if !p.state.HistoryInit {
		p.state.HistoryOpen = w >= 1150
		p.state.HistoryInit = true
	}
	if p.state.HistoryWidth <= 0 {
		p.state.HistoryWidth = 320
	}
	// Viewport need: below 1050 DIP the inspector is not allowed; the graph
	// re-routes to the full-width workspace instead of squeezing.
	showHistory := p.state.HistoryOpen && w >= 1050
	padV, padH := float32(24), float32(32)
	if w < 900 {
		padV, padH = 16, 16
	}
	compact := w < 740

	if showHistory {
		ui.Row(c).Fill().Children(func() {
			ui.Scroll(c).
				Grow(1).
				FillHeight().
				Padding(padV, padH).
				Children(func() {
					p.renderWorkspaceContent(c, compact, redraw)
				})

			components.VerticalResizeHandle(c, components.ResizeHandleProps{
				Width:        p.state.HistoryWidth,
				MinWidth:     260,
				MaxWidth:     600,
				DefaultWidth: 320,
				OnResize: func(newW int) {
					p.state.HistoryWidth = newW
					redraw()
				},
			})

			ui.Column(c).
				Width(float32(p.state.HistoryWidth)).
				FillHeight().
				BorderWidth(0, 0, 0, 2).
				BorderColor(theme.Ink).
				Background(theme.Paper).
				Children(func() {
					p.renderHistoryPanel(c, redraw)
				})
		})
	} else {
		ui.Scroll(c).
			Fill().
			Padding(padV, padH).
			Children(func() {
				p.renderWorkspaceContent(c, compact, redraw)
			})
	}
}

func (p *Plugin) showToast(msg string, redraw func()) {
	p.state.ToastMessage = msg
	if p.state.ToastTimer != nil {
		p.state.ToastTimer.Stop()
	}
	p.state.ToastTimer = time.AfterFunc(3*time.Second, func() {
		p.state.ToastMessage = ""
		redraw()
	})
	redraw()
}

func (p *Plugin) renderWorkspaceContent(c *ui.Context, compact bool, redraw func()) {
	ui.Column(c).
		FillWidth().
		Gap(16).
		Children(func() {
			// 1. Tool Header
			ui.Row(c).Wrap().Justify(ui.SpaceBetween).AlignItems(ui.Center).Gap(12).Children(func() {
				ui.Column(c).Gap(4).MinWidth(200).Grow(1).Children(func() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Text(c, p.t("yt.name")).
							FontSize(24).
							FontWeight(700).
							TextColor(theme.Ink)
					})
					ui.Text(c, "Download video and audio streams into your local library.").
						FontSize(14).
						TextColor(theme.InkMuted)
				})

				w, _ := c.Size()
				if w >= 1050 {
					histLabel := fmt.Sprintf("History (%d)", len(p.History().All()))
					if p.state.HistoryOpen {
						histLabel = "Hide History"
					}
					components.SecondaryButton(c, histLabel, func() {
						p.state.HistoryOpen = !p.state.HistoryOpen
						redraw()
					})
				}
			})

			// 2. Binary Missing Void State
			binPath, binErr := ResolveBinary(p.store.GetString("binary_path"), p.GetDefaultPath())
			if binErr != nil {
				p.renderMissingBinaryCard(c, redraw)
				return
			}

			// 3. Main Workspace Form
			ui.Column(c).
				Border(2, theme.Ink).
				Background(theme.Paper).
				Padding(16).
				Gap(12).
				Children(func() {
					// URL input & Preview row
					ui.Column(c).Gap(6).Children(func() {
						ui.Text(c, "Video or Playlist URL").
							FontSize(13).
							FontWeight(600).
							TextColor(theme.Ink)

						ui.Row(c).Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
							input := ui.TextInput(c, &p.state.URLInput).
								Placeholder("https://www.youtube.com/watch?v=...").
								Label("Video URL").
								Height(36).
								MinWidth(220).
								Radius(0).
								Padding(0, 12).
								Border(2, theme.Ink).
								Background(theme.Paper).
								TextColor(theme.Ink).
								FocusRingColor(theme.Ink).
								Grow(1)

							input.OnChange(func() {
								p.state.InlineError = ""
								p.state.PreviewError = ""
								redraw()
							})

							previewLabel := "Preview"
							if p.state.IsLoadingPreview {
								previewLabel = "Loading..."
							}
							components.SecondaryButton(c, previewLabel, func() {
								if p.state.IsLoadingPreview {
									return
								}
								p.fetchPreview(binPath, redraw)
							})
						})

						if p.state.InlineError != "" {
							ui.Text(c, p.state.InlineError).
								FontSize(12).
								TextColor(theme.Danger)
						}
						if p.state.PreviewError != "" {
							ui.Text(c, p.state.PreviewError).
								FontSize(12).
								TextColor(theme.Danger)
						}
					})

					// Preview Process Progress & Console Output
					loadingPreview, _, _, _, _, previewLogs := p.state.snapshotPreview()
					if loadingPreview || len(previewLogs) > 0 {
						p.renderPreviewProcessCard(c, redraw)
					}

					// Video Preview Card (if metadata loaded)
					if p.state.PreviewMetadata != nil {
						p.renderPreviewCard(c, redraw)
					}

					// Playlist Preview Card (if playlist loaded)
					if p.state.PlaylistMetadata != nil {
						p.renderPlaylistCard(c, binPath, redraw)
					}

					// Bauhaus Options Tabs: Stream & Format | Trim & Post-Process | Network & Auth
					components.Tabs(c, "yt_options_tabs", &p.state.OptionsTab, "Stream & Format", "Trim & Post-Process", "Network & Auth")

					switch p.state.OptionsTab {
					case 1:
						p.renderTrimPostTab(c, redraw)
					case 2:
						p.renderNetworkAuthTab(c, binPath, redraw)
					default:
						p.renderStreamFormatTab(c, compact, redraw)
					}

					// Output folder display & browse
					outDir := p.store.GetString("download_dir")
					displayDir := outDir
					if displayDir == "" {
						displayDir = "~/Downloads"
					}
					ui.Column(c).Gap(6).Children(func() {
						ui.Text(c, "Save Destination").FontSize(13).FontWeight(600).TextColor(theme.Ink)
						ui.Row(c).Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
							ui.Text(c, displayDir).
								FontSize(13).
								TextColor(theme.Ink).
								MinWidth(180).
								Grow(1)

							components.SecondaryButton(c, "Change...", func() {
								go func() {
									paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Directory: true})
									if err == nil && len(paths) > 0 {
										p.store.Set("download_dir", paths[0])
										_ = p.store.Save()
										redraw()
									}
								}()
							})

							if outDir != "" && outDir != "~/Downloads" {
								components.SecondaryButton(c, "Reset to Default", func() {
									p.store.Set("download_dir", "~/Downloads")
									_ = p.store.Save()
									redraw()
								})
							}
						})
					})

					// Live yt-dlp Command Preview Box
					ffmpegBin, ffmpegErr := ResolveFFmpeg(p.store.GetString("ffmpeg_path"), p.GetDefaultPath())
					cmdOpts := p.buildCurrentCommandOptions(binPath, ffmpegBin, p.state.URLInput, false)
					cmdStr := BuildCommandString(cmdOpts)

					ui.Column(c).Gap(4).Children(func() {
						ui.Text(c, "yt-dlp Command Preview").
							FontSize(12).
							FontWeight(600).
							TextColor(theme.InkMuted)

						ui.Box(c).
							Border(1, theme.Ink).
							Background(theme.Ground).
							Padding(6, 10).
							Children(func() {
								ui.Text(c, cmdStr).
									Font("monospace").
									FontSize(11).
									TextColor(theme.Ink)
							})
					})

					// Submit action row
					ui.Row(c).Wrap().Justify(ui.SpaceBetween).AlignItems(ui.Center).Gap(8).Children(func() {
						components.PrimaryButton(c, p.t("yt.download_video"), func() {
							p.startDownload(binPath, redraw)
						})

						if p.state.ActionMessage != "" {
							ui.Text(c, p.state.ActionMessage).
								FontSize(13).
								TextColor(theme.InkMuted)
						}
					})

					if NeedsFFmpeg(cmdOpts) && ffmpegErr != nil {
						ui.Row(c).
							Border(2, theme.Warning).
							Background(theme.Ground).
							Padding(8, 12).
							Gap(8).
							AlignItems(ui.Center).
							Children(func() {
								ui.Box(c).Size(8, 8).Background(theme.Warning)
								ui.Text(c, "ffmpeg not found — required for merging streams, clipping, audio conversion, or embedding.").
									FontSize(12).
									TextColor(theme.Ink).
									Grow(1)
							})
					}
				})

			// 4. Active Job Live Card (batch or single)
			if len(p.state.BatchJobIDs) > 1 && p.queue != nil {
				p.renderBatchJobCard(c, redraw)
			} else if p.state.ActiveJobID != "" && p.queue != nil {
				if rec, ok := p.queue.GetRecord(p.state.ActiveJobID); ok {
					p.renderJobCard(c, rec, redraw)
				}
			}
		})
}

func (p *Plugin) renderHistoryPanel(c *ui.Context, redraw func()) {
	items := p.History().All()
	cardItems := make([]components.HistoryCardItem, len(items))
	for i, it := range items {
		var metaParts []string
		if it.Channel != "" {
			metaParts = append(metaParts, it.Channel)
		}
		if it.Duration != "" {
			metaParts = append(metaParts, it.Duration)
		}
		metaParts = append(metaParts, it.DownloadedAt.Format("15:04"))

		cardItems[i] = components.HistoryCardItem{
			ID:       it.ID,
			Title:    it.Title,
			Subtitle: strings.Join(metaParts, " · "),
			FilePath: it.FilePath,
		}
	}

	components.HistoryPanel(c, components.HistoryPanelProps{
		Title:         "Download History",
		Count:         len(items),
		Items:         cardItems,
		ToastMessage:  p.state.ToastMessage,
		EmptyTitle:    "No downloads yet",
		EmptySubtitle: "Completed tasks will appear here.",
		OnClearAll: func() {
			_ = p.History().ClearAll()
			redraw()
		},
		OnDismissToast: func() {
			p.state.ToastMessage = ""
			if p.state.ToastTimer != nil {
				p.state.ToastTimer.Stop()
			}
			redraw()
		},
		OnShow: func(filePath string) {
			fi, err := os.Stat(filePath)
			if err != nil {
				p.showToast("File was moved or deleted", redraw)
				return
			}
			if !fi.IsDir() {
				go mygo.Shell.ShowItemInFolder(filePath)
			} else {
				go func() { _ = mygo.Shell.OpenPath(filePath) }()
			}
		},
		OnDelete: func(id string) {
			_ = p.History().Delete(id)
			redraw()
		},
	})
}

func (s *ViewState) startPreviewRun(cancel context.CancelFunc) {
	s.previewMu.Lock()
	if s.previewCancel != nil {
		s.previewCancel()
	}
	s.previewCancel = cancel
	s.IsLoadingPreview = true
	s.PreviewProgress = 10
	s.PreviewStatus = "Starting yt-dlp preview..."
	s.PreviewFailed = false
	s.PreviewCanceled = false
	s.PreviewLogs = nil
	s.previewMu.Unlock()
}

func (s *ViewState) cancelPreview() {
	s.previewMu.Lock()
	cancel := s.previewCancel
	s.previewMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *ViewState) setPreviewProgress(pct float64, status string) {
	s.previewMu.Lock()
	s.PreviewProgress = pct
	if status != "" {
		s.PreviewStatus = status
	}
	s.previewMu.Unlock()
}

func (s *ViewState) appendPreviewLog(line string) {
	if line == "" {
		return
	}
	s.previewMu.Lock()
	s.PreviewLogs = append(s.PreviewLogs, line)
	if len(s.PreviewLogs) > 200 {
		s.PreviewLogs = s.PreviewLogs[len(s.PreviewLogs)-200:]
	}
	s.previewMu.Unlock()
}

func (s *ViewState) finishPreviewRun(pct float64, status string, failed, canceled bool) {
	s.previewMu.Lock()
	s.IsLoadingPreview = false
	s.previewCancel = nil
	s.PreviewProgress = pct
	s.PreviewStatus = status
	s.PreviewFailed = failed
	s.PreviewCanceled = canceled
	s.previewMu.Unlock()
}

func (s *ViewState) dismissPreviewLogs() {
	s.previewMu.Lock()
	s.PreviewLogs = nil
	s.PreviewProgress = 0
	s.PreviewStatus = ""
	s.PreviewFailed = false
	s.PreviewCanceled = false
	s.previewMu.Unlock()
}

func (s *ViewState) snapshotPreview() (loading bool, progress float64, status string, failed, canceled bool, logs []string) {
	s.previewMu.Lock()
	defer s.previewMu.Unlock()
	loading = s.IsLoadingPreview
	progress = s.PreviewProgress
	status = s.PreviewStatus
	failed = s.PreviewFailed
	canceled = s.PreviewCanceled
	if len(s.PreviewLogs) > 0 {
		logs = make([]string, len(s.PreviewLogs))
		copy(logs, s.PreviewLogs)
	}
	return
}

func (p *Plugin) fetchPreview(binPath string, redraw func()) {
	sanitized, err := ParseURL(p.state.URLInput)
	if err != nil {
		p.state.InlineError = err.Error()
		redraw()
		return
	}
	p.state.InlineError = ""
	p.state.PreviewError = ""

	ctx, cancel := context.WithCancel(context.Background())
	p.state.startPreviewRun(cancel)
	redraw()

	go func() {
		defer cancel()
		onLog := func(line string) {
			p.state.appendPreviewLog(line)
			redraw()
		}

		if IsPlaylistURL(sanitized) {
			p.state.appendPreviewLog(fmt.Sprintf("[cmd] %s --flat-playlist --dump-single-json %s", quoteArg(binPath), quoteArg(sanitized)))
			p.state.setPreviewProgress(25, "Fetching playlist metadata...")
			redraw()

			pm, err := FetchPlaylistMetadataWithLogs(ctx, binPath, sanitized, onLog)
			if err != nil {
				if ctx.Err() != nil {
					p.state.appendPreviewLog("[preview] Canceled")
					p.state.finishPreviewRun(0, "Preview canceled.", false, true)
					redraw()
					return
				}
				p.state.PreviewError = fmt.Sprintf("Failed to load playlist: %v", err)
				p.state.appendPreviewLog("[error] " + p.state.PreviewError)
				p.state.finishPreviewRun(0, p.state.PreviewError, true, false)
				redraw()
				return
			}
			p.state.PreviewMetadata = nil
			p.state.PreviewThumbnail = nil
			p.state.PlaylistList = ui.ListState{}
			p.state.PlaylistMetadata = pm
			p.state.SelectedEntries = make(map[string]bool, len(pm.Entries))
			for _, e := range pm.Entries {
				p.state.SelectedEntries[e.URL] = true
			}
			p.state.appendPreviewLog(fmt.Sprintf("[preview] Loaded playlist %q (%d items)", pm.Title, len(pm.Entries)))
			p.state.finishPreviewRun(100, fmt.Sprintf("Loaded playlist (%d items)", len(pm.Entries)), false, false)
			redraw()
			return
		}

		p.state.PlaylistMetadata = nil
		p.state.appendPreviewLog(fmt.Sprintf("[cmd] %s --dump-single-json --no-playlist %s", quoteArg(binPath), quoteArg(sanitized)))
		p.state.setPreviewProgress(25, "Fetching video metadata...")
		redraw()

		meta, err := FetchMetadataWithLogs(ctx, binPath, sanitized, onLog)
		if err != nil {
			if ctx.Err() != nil {
				p.state.appendPreviewLog("[preview] Canceled")
				p.state.finishPreviewRun(0, "Preview canceled.", false, true)
				redraw()
				return
			}
			p.state.PreviewError = fmt.Sprintf("Failed to load preview: %v", err)
			p.state.appendPreviewLog("[error] " + p.state.PreviewError)
			p.state.finishPreviewRun(0, p.state.PreviewError, true, false)
			redraw()
			return
		}

		p.state.PreviewMetadata = meta
		p.state.VideoOptions = ExtractVideoOptions(meta.Formats)
		p.state.AudioOptions = ExtractAudioOptions(meta.Formats)

		if len(p.state.VideoOptions) > 0 {
			p.state.SelectedVideo = p.state.VideoOptions[0].Value
		}
		if len(p.state.AudioOptions) > 0 {
			p.state.SelectedAudio = p.state.AudioOptions[0].Value
		}

		p.state.appendPreviewLog(fmt.Sprintf("[preview] Loaded %q (%d formats)", meta.Title, len(meta.Formats)))

		if meta.ThumbnailURL != "" {
			p.state.setPreviewProgress(75, "Downloading thumbnail...")
			p.state.appendPreviewLog("[preview] Fetching thumbnail...")
			redraw()
			imgBytes, tErr := FetchThumbnail(ctx, meta.ThumbnailURL)
			if tErr == nil && len(imgBytes) > 0 {
				imgSrc, dErr := ui.DecodeImage(imgBytes)
				if dErr == nil {
					p.state.PreviewThumbnail = imgSrc
				}
			}
		}

		p.state.finishPreviewRun(100, "Preview ready.", false, false)
		redraw()
	}()
}

func (p *Plugin) renderPreviewProcessCard(c *ui.Context, redraw func()) {
	loading, progress, status, failed, canceled, logs := p.state.snapshotPreview()
	if !loading && len(logs) == 0 {
		return
	}

	ui.Column(c).
		Border(2, theme.Ink).
		Background(theme.Paper).
		Padding(16).
		Gap(10).
		Children(func() {
			ui.Row(c).Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "Preview Process").
					FontSize(14).
					FontWeight(700).
					TextColor(theme.Ink)

				statusLabel := "LOADING"
				statusBg := theme.Accent
				switch {
				case failed:
					statusLabel = "FAILED"
					statusBg = theme.Danger
				case canceled:
					statusLabel = "CANCELED"
					statusBg = theme.Warning
				case !loading:
					statusLabel = "DONE"
					statusBg = theme.Success
				}

				ui.Box(c).
					Padding(2, 8).
					Border(1, theme.Ink).
					Background(statusBg).
					Children(func() {
						ui.Text(c, statusLabel).
							FontSize(11).
							FontWeight(700).
							TextColor(theme.OnStatus)
					})
			})

			components.ProgressBar(c, progress, 10)

			ui.Row(c).Wrap().Justify(ui.SpaceBetween).AlignItems(ui.Center).Gap(8).Children(func() {
				msg := status
				if msg == "" && loading {
					msg = fmt.Sprintf("%.0f%%", progress)
				}
				ui.Text(c, msg).
					FontSize(12).
					TextColor(theme.Ink).
					MinWidth(180).
					Grow(1)

				if loading {
					components.SecondaryButton(c, "Cancel", func() {
						p.state.cancelPreview()
						redraw()
					})
				} else {
					components.SecondaryButton(c, "Dismiss", func() {
						p.state.dismissPreviewLogs()
						redraw()
					})
				}
			})

			if len(logs) > 0 {
				renderConsoleOutputBox(c, fmt.Sprintf("Preview Console Output (%d lines)", len(logs)), &p.state.ShowPreviewLogs, logs, redraw)
			}
		})
}

func (p *Plugin) renderPreviewCard(c *ui.Context, redraw func()) {
	meta := p.state.PreviewMetadata
	if meta == nil {
		return
	}

	ui.Column(c).
		Border(2, theme.Ink).
		Background(theme.Ground).
		Padding(16).
		Gap(12).
		Children(func() {
			// Title bar with dismiss button
			ui.Row(c).Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Box(c).Size(10, 10).Background(theme.DeepSage)
					ui.Text(c, "Video Preview").
						FontSize(13).
						FontWeight(700).
						TextColor(theme.Ink)
				})

				components.SecondaryButton(c, "Dismiss", func() {
					p.state.PreviewMetadata = nil
					p.state.PreviewThumbnail = nil
					p.state.VideoOptions = DefaultVideoOptions()
					p.state.AudioOptions = DefaultAudioOptions()
					p.state.SelectedVideo = "bestvideo"
					p.state.SelectedAudio = "bestaudio"
					redraw()
				})
			})

			// Content: Thumbnail + Info
			ui.Row(c).Wrap().Gap(16).AlignItems(ui.Start).Children(func() {
				if p.state.PreviewThumbnail != nil {
					ui.Box(c).
						Width(160).
						Height(90).
						Border(1, theme.Ink).
						Children(func() {
							ui.Image(c, p.state.PreviewThumbnail).Fill()
						})
				}

				ui.Column(c).MinWidth(200).Grow(1).Gap(4).Children(func() {
					ui.Text(c, meta.Title).
						FontSize(14).
						FontWeight(700).
						TextColor(theme.Ink)

					if meta.Channel != "" {
						ui.Text(c, meta.Channel).
							FontSize(12).
							TextColor(theme.InkMuted)
					}

					var details []string
					if meta.DurationString != "" {
						details = append(details, meta.DurationString)
					}
					if meta.ViewCount > 0 {
						details = append(details, formatViews(meta.ViewCount))
					}
					if len(details) > 0 {
						ui.Text(c, strings.Join(details, " · ")).
							FontSize(12).
							TextColor(theme.Ink)
					}
				})
			})
		})
}

func (p *Plugin) renderMissingBinaryCard(c *ui.Context, redraw func()) {
	ui.Column(c).
		Border(2, theme.Warning).
		Background(theme.Paper).
		Padding(24).
		Gap(16).
		Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Size(12, 12).Background(theme.Warning)
				ui.Text(c, "Engine Executable Missing").
					FontSize(16).
					FontWeight(700).
					TextColor(theme.Ink)
			})

			ui.Text(c, "YouTube Downloader requires the yt-dlp executable to extract and download streams. Choose a location to save the official standalone executable.").
				FontSize(14).
				TextColor(theme.Ink)

			defaultTarget := p.DefaultBinaryPath()
			ui.Text(c, fmt.Sprintf(p.t("yt.default_path_info"), defaultTarget)).
				FontSize(12).
				TextColor(theme.InkMuted)

			if p.state.IsDownloadingBin {
				ui.Column(c).Gap(6).Children(func() {
					ui.Text(c, fmt.Sprintf("Downloading yt-dlp: %s (%.1f%%)", p.state.BinDownloadMsg, p.state.BinDownloadProgress)).
						FontSize(13).
						FontWeight(600).
						TextColor(theme.InkMuted)

					components.ProgressBar(c, p.state.BinDownloadProgress, 8)
				})
			} else {
				ui.Row(c).Wrap().Gap(12).AlignItems(ui.Center).Children(func() {
					components.PrimaryButton(c, p.t("yt.store_default_path"), func() {
						go func() {
							p.state.IsDownloadingBin = true
							p.state.BinDownloadProgress = 0
							p.state.BinDownloadMsg = "Connecting..."
							redraw()

							target, dlErr := p.DownloadBinaryWithProgress(context.Background(), defaultTarget, func(pr job.Progress) {
								p.state.BinDownloadProgress = pr.Done
								p.state.BinDownloadMsg = pr.Message
								redraw()
							})

							p.state.IsDownloadingBin = false
							if dlErr != nil {
								p.state.ActionMessage = fmt.Sprintf("Download failed: %v", dlErr)
							} else {
								p.state.ActionMessage = fmt.Sprintf("✓ Stored at: %s", target)
							}
							redraw()
						}()
					})

					components.SecondaryButton(c, "Download yt-dlp...", func() {
						go func() {
							savePath, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
								Title:       "Save yt-dlp executable",
								DefaultPath: defaultTarget,
							})
							if err != nil || savePath == "" {
								return
							}

							p.state.IsDownloadingBin = true
							p.state.BinDownloadProgress = 0
							p.state.BinDownloadMsg = "Connecting..."
							redraw()

							target, dlErr := p.DownloadBinaryWithProgress(context.Background(), savePath, func(pr job.Progress) {
								p.state.BinDownloadProgress = pr.Done
								p.state.BinDownloadMsg = pr.Message
								redraw()
							})

							p.state.IsDownloadingBin = false
							if dlErr != nil {
								p.state.ActionMessage = fmt.Sprintf("Download failed: %v", dlErr)
							} else {
								p.state.ActionMessage = fmt.Sprintf("✓ Stored at: %s", target)
							}
							redraw()
						}()
					})

					if p.state.ActionMessage != "" {
						ui.Text(c, p.state.ActionMessage).
							FontSize(13).
							TextColor(theme.Ink)
					}
				})
			}
		})
}

func (p *Plugin) renderJobCard(c *ui.Context, rec job.JobRecord, redraw func()) {
	ui.Column(c).
		Border(2, theme.Ink).
		Background(theme.Paper).
		Padding(20).
		Gap(12).
		Children(func() {
			// Job Title & Status badge
			ui.Row(c).Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "Active Task").
					FontSize(15).
					FontWeight(700).
					TextColor(theme.Ink)

				statusLabel := "RUNNING"
				statusBg := theme.Accent
				statusFg := theme.OnStatus
				switch rec.Status {
				case job.StatusQueued:
					statusLabel = "QUEUED"
					statusBg = theme.Quiet
					statusFg = theme.Ink
				case job.StatusSucceeded:
					statusLabel = "DONE"
					statusBg = theme.Success
				case job.StatusFailed:
					statusLabel = "FAILED"
					statusBg = theme.Danger
				case job.StatusCanceled:
					statusLabel = "CANCELED"
					statusBg = theme.Warning
				}

				ui.Box(c).
					Padding(2, 8).
					Border(1, theme.Ink).
					Background(statusBg).
					Children(func() {
						ui.Text(c, statusLabel).
							FontSize(11).
							FontWeight(700).
							TextColor(statusFg)
					})
			})

			// Progress bar
			components.ProgressBar(c, rec.Progress.Done, 12)

			// Metrics and Cancel
			ui.Row(c).Wrap().Justify(ui.SpaceBetween).AlignItems(ui.Center).Gap(8).Children(func() {
				msg := rec.Progress.Message
				if msg == "" && rec.Status == job.StatusRunning {
					msg = fmt.Sprintf("%.1f%%", rec.Progress.Done)
				}
				if rec.Status == job.StatusFailed && rec.Error != "" {
					msg = rec.Error
				}
				if rec.Status == job.StatusSucceeded {
					msg = "Download finished."
					p.recordCompletedJobHistory(rec, "", "", "", "")
				}

				ui.Text(c, msg).
					FontSize(13).
					TextColor(theme.Ink).
					MinWidth(180).
					Grow(1)

				if rec.Status == job.StatusRunning || rec.Status == job.StatusQueued {
					components.SecondaryButton(c, "Cancel", func() {
						p.queue.Cancel(rec.ID)
						redraw()
					})
				} else {
					ui.Row(c).Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
						if rec.Status == job.StatusSucceeded && len(rec.Result.Outputs) > 0 {
							targetPath := rec.Result.Outputs[0]
							components.SecondaryButton(c, "Show in Explorer", func() {
								fi, err := os.Stat(targetPath)
								if err != nil {
									p.showToast("File was moved or deleted", redraw)
									return
								}
								if !fi.IsDir() {
									go mygo.Shell.ShowItemInFolder(targetPath)
								} else {
									go func() { _ = mygo.Shell.OpenPath(targetPath) }()
								}
							})
						}

						components.SecondaryButton(c, "Dismiss", func() {
							p.state.ActiveJobID = ""
							p.state.BatchJobIDs = nil
							redraw()
						})
					})
				}
			})

			// Live Console Output Logs
			if len(rec.Logs) > 0 {
				renderConsoleOutputBox(c, fmt.Sprintf("Console Output (%d lines)", len(rec.Logs)), &p.state.ShowLogs, rec.Logs, redraw)
			}
		})
}

func renderConsoleOutputBox(c *ui.Context, title string, showLogs *bool, logs []string, redraw func()) {
	ui.Column(c).Gap(6).Children(func() {
		ui.Row(c).Wrap().Justify(ui.SpaceBetween).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, title).
				FontSize(12).
				FontWeight(600).
				TextColor(theme.InkMuted)

			toggleText := "Hide Logs"
			if !*showLogs {
				toggleText = "Show Logs"
			}
			components.SecondaryButton(c, toggleText, func() {
				*showLogs = !*showLogs
				redraw()
			})
		})

		if *showLogs {
			ui.Box(c).
				Height(140).
				Border(1, theme.Ink).
				Background(theme.Ground).
				Padding(8).
				Children(func() {
					ui.Scroll(c).Fill().Children(func() {
						ui.Column(c).Gap(2).Children(func() {
							for _, line := range logs {
								ui.Text(c, line).
									Font("monospace").
									FontSize(11).
									TextColor(theme.Ink)
							}
						})
					})
				})
		}
	})
}

func (p *Plugin) recordCompletedJobHistory(rec job.JobRecord, fallbackURL, fallbackTitle, fallbackChannel, fallbackDuration string) {
	if p.state.RecordedJobIDs[rec.ID] {
		return
	}
	p.state.RecordedJobIDs[rec.ID] = true

	title := fallbackTitle
	channel := fallbackChannel
	duration := fallbackDuration
	if title == "" {
		title = "YouTube Video"
	}
	if p.state.PreviewMetadata != nil && fallbackTitle == "" {
		if p.state.PreviewMetadata.Title != "" {
			title = p.state.PreviewMetadata.Title
		}
		channel = p.state.PreviewMetadata.Channel
		duration = p.state.PreviewMetadata.DurationString
	}
	filePath := ""
	if len(rec.Result.Outputs) > 0 {
		filePath = rec.Result.Outputs[0]
		if title == "YouTube Video" {
			title = filepath.Base(filePath)
		}
	}
	targetURL := fallbackURL
	if targetURL == "" {
		targetURL = p.state.URLInput
	}
	_ = p.History().Add(HistoryItem{
		ID:           string(rec.ID),
		URL:          targetURL,
		Title:        title,
		Channel:      channel,
		Duration:     duration,
		FilePath:     filePath,
		DownloadedAt: time.Now(),
	})
}

func (p *Plugin) renderBatchJobCard(c *ui.Context, redraw func()) {
	total := len(p.state.BatchJobIDs)
	if total == 0 || p.queue == nil {
		return
	}

	records := make([]job.JobRecord, 0, total)
	var sumPct float64
	var succeededCount, runningCount, queuedCount, failedCount, canceledCount int
	var activeMsg string
	var lastErr string
	var lastSucceededOut string
	var aggregatedLogs []string

	// Build lookup for playlist entry metadata by title/index
	var playlistEntries []PlaylistEntry
	if p.state.PlaylistMetadata != nil {
		for _, e := range p.state.PlaylistMetadata.Entries {
			if p.state.SelectedEntries[e.URL] {
				playlistEntries = append(playlistEntries, e)
			}
		}
	}

	for i, id := range p.state.BatchJobIDs {
		rec, ok := p.queue.GetRecord(id)
		if !ok {
			continue
		}
		records = append(records, rec)

		switch rec.Status {
		case job.StatusSucceeded:
			succeededCount++
			sumPct += 100
			if len(rec.Result.Outputs) > 0 {
				lastSucceededOut = rec.Result.Outputs[0]
			}
			var entryURL, entryTitle, entryChannel, entryDur string
			if i < len(playlistEntries) {
				entryURL = playlistEntries[i].URL
				entryTitle = playlistEntries[i].Title
				entryChannel = playlistEntries[i].Channel
				entryDur = playlistEntries[i].DurationString
			} else if rec.Tool != "" {
				entryTitle = rec.Tool
			}
			p.recordCompletedJobHistory(rec, entryURL, entryTitle, entryChannel, entryDur)
		case job.StatusRunning:
			runningCount++
			sumPct += rec.Progress.Done
			if rec.Progress.Message != "" {
				activeMsg = fmt.Sprintf("[#%d] %s", i+1, rec.Progress.Message)
			}
		case job.StatusQueued:
			queuedCount++
		case job.StatusFailed:
			failedCount++
			sumPct += 100
			if rec.Error != "" {
				lastErr = rec.Error
			}
		case job.StatusCanceled:
			canceledCount++
			sumPct += 100
		}

		for _, line := range rec.Logs {
			aggregatedLogs = append(aggregatedLogs, fmt.Sprintf("[#%d] %s", i+1, line))
		}
	}

	if len(records) == 0 {
		return
	}

	overallPct := sumPct / float64(len(records))
	anyActive := runningCount > 0 || queuedCount > 0

	statusLabel := "RUNNING"
	statusBg := theme.Accent
	statusFg := theme.OnStatus
	switch {
	case runningCount > 0:
		statusLabel = "RUNNING"
		statusBg = theme.Accent
	case queuedCount > 0:
		statusLabel = "QUEUED"
		statusBg = theme.Quiet
		statusFg = theme.Ink
	case failedCount > 0:
		statusLabel = "FAILED"
		statusBg = theme.Danger
	case canceledCount == len(records):
		statusLabel = "CANCELED"
		statusBg = theme.Warning
	default:
		statusLabel = "DONE"
		statusBg = theme.Success
	}

	ui.Column(c).
		Border(2, theme.Ink).
		Background(theme.Paper).
		Padding(20).
		Gap(12).
		Children(func() {
			ui.Row(c).Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
				ui.Text(c, fmt.Sprintf("Batch Download (%d/%d)", succeededCount, len(records))).
					FontSize(15).
					FontWeight(700).
					TextColor(theme.Ink)

				ui.Box(c).
					Padding(2, 8).
					Border(1, theme.Ink).
					Background(statusBg).
					Children(func() {
						ui.Text(c, statusLabel).
							FontSize(11).
							FontWeight(700).
							TextColor(statusFg)
					})
			})

			components.ProgressBar(c, overallPct, 12)

			ui.Row(c).Wrap().Justify(ui.SpaceBetween).AlignItems(ui.Center).Gap(8).Children(func() {
				summary := fmt.Sprintf("%.0f%% · %d done, %d running, %d queued", overallPct, succeededCount, runningCount, queuedCount)
				if failedCount > 0 {
					summary += fmt.Sprintf(", %d failed", failedCount)
				}
				if activeMsg != "" {
					summary += " · " + activeMsg
				} else if !anyActive && lastErr != "" && failedCount > 0 {
					summary = lastErr
				}

				ui.Text(c, summary).
					FontSize(13).
					TextColor(theme.Ink).
					MinWidth(180).
					Grow(1)

				if anyActive {
					components.SecondaryButton(c, "Cancel All", func() {
						for _, id := range p.state.BatchJobIDs {
							p.queue.Cancel(id)
						}
						redraw()
					})
				} else {
					ui.Row(c).Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
						if lastSucceededOut != "" {
							targetPath := lastSucceededOut
							components.SecondaryButton(c, "Show in Explorer", func() {
								fi, err := os.Stat(targetPath)
								if err != nil {
									p.showToast("File was moved or deleted", redraw)
									return
								}
								if !fi.IsDir() {
									go mygo.Shell.ShowItemInFolder(targetPath)
								} else {
									go func() { _ = mygo.Shell.OpenPath(targetPath) }()
								}
							})
						}
						components.SecondaryButton(c, "Dismiss", func() {
							p.state.ActiveJobID = ""
							p.state.BatchJobIDs = nil
							redraw()
						})
					})
				}
			})

			if len(aggregatedLogs) > 0 {
				if len(aggregatedLogs) > 400 {
					aggregatedLogs = aggregatedLogs[len(aggregatedLogs)-400:]
				}
				renderConsoleOutputBox(c, fmt.Sprintf("Console Output (%d lines)", len(aggregatedLogs)), &p.state.ShowLogs, aggregatedLogs, redraw)
			}
		})
}

func (p *Plugin) resolveOutputDir() string {
	out := p.store.GetString("download_dir")
	if out == "" || out == "~/Downloads" {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, "Downloads")
		}
		return os.TempDir()
	}
	if strings.HasPrefix(out, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, out[2:])
		}
	}
	return out
}

func (p *Plugin) selectedSponsorBlockCats() []string {
	var cats []string
	for _, cat := range DefaultSponsorBlockCategories() {
		if p.state.SponsorBlockCats[cat] {
			cats = append(cats, cat)
		}
	}
	return cats
}

func (p *Plugin) buildCurrentCommandOptions(binPath, ffmpegPath, targetURL string, isPlaylistItem bool) CommandOptions {
	embedSubs := p.state.EmbedSubs || p.store.GetBool("embed_subtitles")
	cookiesBrowser := p.state.CookiesFromBrowser
	if cookiesBrowser == "" || cookiesBrowser == "none" {
		if s := p.store.GetString("cookies_from_browser"); s != "" && s != "none" {
			cookiesBrowser = s
		}
	}
	proxyURL := p.state.ProxyURL
	if proxyURL == "" {
		proxyURL = p.store.GetString("proxy_url")
	}
	rateLimit := p.state.RateLimit
	if rateLimit == "" {
		rateLimit = p.store.GetString("rate_limit")
	}
	fragStr := p.state.ConcurrentFragments
	if fragStr == "" || fragStr == "1" {
		if s := p.store.GetString("concurrent_fragments"); s != "" {
			fragStr = s
		}
	}
	frags, _ := strconv.Atoi(fragStr)

	return CommandOptions{
		BinaryPath:          binPath,
		FFmpegPath:          ffmpegPath,
		URL:                 targetURL,
		OutputDir:           p.resolveOutputDir(),
		VideoStream:         p.state.SelectedVideo,
		AudioStream:         p.state.SelectedAudio,
		OutputFormat:        p.state.SelectedContainer,
		AudioQuality:        p.state.SelectedAudioQuality,
		EmbedSubs:           embedSubs,
		SubLangs:            p.state.SubLangs,
		WriteAutoSubs:       p.state.WriteAutoSubs,
		EmbedThumbnail:      p.state.EmbedThumbnail,
		EmbedChapters:       p.state.EmbedChapters,
		EmbedMetadata:       p.state.EmbedMetadata,
		SponsorBlockMode:    p.state.SponsorBlockMode,
		SponsorBlockCats:    p.selectedSponsorBlockCats(),
		TimeRangeStart:      p.state.TimeRangeStart,
		TimeRangeEnd:        p.state.TimeRangeEnd,
		ForceKeyframesCuts:  p.state.ForceKeyframesCuts,
		CookiesFromBrowser:  cookiesBrowser,
		CookiesFile:         p.state.CookiesFile,
		ProxyURL:            proxyURL,
		RateLimit:           rateLimit,
		ConcurrentFragments: frags,
		CustomFilename:      p.state.CustomFilename,
		IsPlaylistItem:      isPlaylistItem,
	}
}

func (p *Plugin) validateAdvancedOptions() bool {
	p.state.TimeRangeError = ""
	p.state.NetworkError = ""
	if _, err := ParseTimeRange(p.state.TimeRangeStart, p.state.TimeRangeEnd); err != nil {
		p.state.TimeRangeError = err.Error()
		p.state.OptionsTab = 1
		return false
	}
	if _, err := ParseProxyURL(p.state.ProxyURL); err != nil {
		p.state.NetworkError = err.Error()
		p.state.OptionsTab = 2
		return false
	}
	if _, err := ParseRateLimit(p.state.RateLimit); err != nil {
		p.state.NetworkError = err.Error()
		p.state.OptionsTab = 2
		return false
	}
	if _, err := ValidateCookiesFile(p.state.CookiesFile); err != nil {
		p.state.NetworkError = err.Error()
		p.state.OptionsTab = 2
		return false
	}
	return true
}

func (p *Plugin) newDownloadJob(opts CommandOptions) DownloadJob {
	return DownloadJob{
		URL:                 opts.URL,
		BinaryPath:          opts.BinaryPath,
		FFmpegPath:          opts.FFmpegPath,
		OutputDir:           opts.OutputDir,
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
		IsPlaylistItem:      opts.IsPlaylistItem,
	}
}

func (p *Plugin) startDownload(binPath string, redraw func()) {
	sanitized, err := ParseURL(p.state.URLInput)
	if err != nil {
		p.state.InlineError = err.Error()
		redraw()
		return
	}
	if !p.validateAdvancedOptions() {
		redraw()
		return
	}

	p.state.InlineError = ""
	ffmpegBin, _ := ResolveFFmpeg(p.store.GetString("ffmpeg_path"), p.GetDefaultPath())
	opts := p.buildCurrentCommandOptions(binPath, ffmpegBin, sanitized, false)

	if p.queue != nil {
		id := p.enqueueJob("YouTube Downloader", p.newDownloadJob(opts))
		p.state.BatchJobIDs = nil
		p.state.ActiveJobID = id
		redraw()
	}
}

func (p *Plugin) startPlaylistDownload(binPath string, redraw func()) {
	pm := p.state.PlaylistMetadata
	if pm == nil || p.queue == nil {
		return
	}
	if !p.validateAdvancedOptions() {
		redraw()
		return
	}
	ffmpegBin, _ := ResolveFFmpeg(p.store.GetString("ffmpeg_path"), p.GetDefaultPath())
	p.state.BatchJobIDs = nil
	for _, entry := range pm.Entries {
		if !p.state.SelectedEntries[entry.URL] {
			continue
		}
		opts := p.buildCurrentCommandOptions(binPath, ffmpegBin, entry.URL, true)
		title := entry.Title
		if title == "" {
			title = "Playlist Item"
		}
		id := p.enqueueJob(title, p.newDownloadJob(opts))
		p.state.BatchJobIDs = append(p.state.BatchJobIDs, id)
	}
	if len(p.state.BatchJobIDs) > 0 {
		p.state.ActiveJobID = p.state.BatchJobIDs[0]
		p.state.ActionMessage = fmt.Sprintf("Queued %d playlist items", len(p.state.BatchJobIDs))
	}
	redraw()
}

func bauhausToggleChip(c *ui.Context, label string, active bool, onClick func()) {
	bg := theme.Paper
	fg := theme.Ink
	if active {
		bg = theme.Accent
		fg = theme.OnField
	}
	btn := ui.ButtonBase(c).
		Radius(0).
		Padding(6, 12).
		Border(2, theme.Ink).
		Background(bg).
		TextColor(fg).
		FocusRingColor(theme.Ink).
		OnClick(onClick)
	btn.Children(func() {
		ui.Text(c, label).FontSize(12).FontWeight(600).TextColor(fg).SingleLine()
	})
}

func (p *Plugin) renderVideoStreamSelect(c *ui.Context, grow, minW float32, redraw func()) {
	ui.Column(c).MinWidth(minW).Grow(grow).Gap(6).Children(func() {
		ui.Text(c, "Video Stream").FontSize(13).FontWeight(600).TextColor(theme.Ink)
		labels, valToLabel, labelToVal := mapOptions(p.state.VideoOptions)
		curLabel := valToLabel[p.state.SelectedVideo]
		if curLabel == "" && len(labels) > 0 {
			curLabel = labels[0]
			p.state.SelectedVideo = labelToVal[curLabel]
		}
		sel := ui.Select(c, &curLabel, labels).
			Height(36).
			MinWidth(0).
			FillWidth().
			Padding(0, 10).
			Border(2, theme.Ink).
			Background(theme.Paper)
		sel.OnChange(func() {
			p.state.SelectedVideo = labelToVal[curLabel]
			redraw()
		})
	})
}

func (p *Plugin) renderAudioStreamSelect(c *ui.Context, grow, minW float32, redraw func()) {
	ui.Column(c).MinWidth(minW).Grow(grow).Gap(6).Children(func() {
		ui.Text(c, "Audio Stream").FontSize(13).FontWeight(600).TextColor(theme.Ink)
		labels, valToLabel, labelToVal := mapOptions(p.state.AudioOptions)
		curLabel := valToLabel[p.state.SelectedAudio]
		if curLabel == "" && len(labels) > 0 {
			curLabel = labels[0]
			p.state.SelectedAudio = labelToVal[curLabel]
		}
		sel := ui.Select(c, &curLabel, labels).
			Height(36).
			MinWidth(0).
			FillWidth().
			Padding(0, 10).
			Border(2, theme.Ink).
			Background(theme.Paper)
		sel.OnChange(func() {
			p.state.SelectedAudio = labelToVal[curLabel]
			redraw()
		})
	})
}

func (p *Plugin) renderContainerSelect(c *ui.Context, grow, minW float32, redraw func()) {
	ui.Column(c).MinWidth(minW).Grow(grow).Gap(6).Children(func() {
		ui.Text(c, "Format").FontSize(13).FontWeight(600).TextColor(theme.Ink)
		containers := []string{"mp4", "mkv", "webm", "mp3", "m4a", "opus", "flac", "wav"}
		sel := ui.Select(c, &p.state.SelectedContainer, containers).
			Height(36).
			MinWidth(0).
			FillWidth().
			Padding(0, 10).
			Border(2, theme.Ink).
			Background(theme.Paper)
		sel.OnChange(func() {
			redraw()
		})
	})
}

func (p *Plugin) renderAudioQualitySelect(c *ui.Context, grow, minW float32, redraw func()) {
	ui.Column(c).MinWidth(minW).Grow(grow).Gap(6).Children(func() {
		ui.Text(c, "Audio Quality").FontSize(13).FontWeight(600).TextColor(theme.Ink)
		qOpts := DefaultAudioQualityOptions()
		labels, valToLabel, labelToVal := mapOptions(qOpts)
		curLabel := valToLabel[p.state.SelectedAudioQuality]
		if curLabel == "" && len(labels) > 0 {
			curLabel = labels[0]
			p.state.SelectedAudioQuality = labelToVal[curLabel]
		}
		sel := ui.Select(c, &curLabel, labels).
			Height(36).
			MinWidth(0).
			FillWidth().
			Padding(0, 10).
			Border(2, theme.Ink).
			Background(theme.Paper)
		sel.OnChange(func() {
			p.state.SelectedAudioQuality = labelToVal[curLabel]
			redraw()
		})
	})
}

func (p *Plugin) renderStreamFormatTab(c *ui.Context, compact bool, redraw func()) {
	ui.Column(c).Border(2, theme.Ink).Background(theme.Paper).Padding(12).Gap(10).Children(func() {
		if compact {
			ui.Row(c).Wrap().Gap(12).AlignItems(ui.Start).Children(func() {
				p.renderVideoStreamSelect(c, 1, 160, redraw)
				p.renderAudioStreamSelect(c, 1, 160, redraw)
			})
			ui.Row(c).Wrap().Gap(12).AlignItems(ui.Start).Children(func() {
				p.renderContainerSelect(c, 1, 130, redraw)
				p.renderAudioQualitySelect(c, 1, 130, redraw)
			})
		} else {
			ui.Row(c).Wrap().Gap(12).AlignItems(ui.Start).Children(func() {
				p.renderVideoStreamSelect(c, 3, 140, redraw)
				p.renderAudioStreamSelect(c, 2, 140, redraw)
				p.renderContainerSelect(c, 1, 110, redraw)
				p.renderAudioQualitySelect(c, 1, 110, redraw)
			})
		}

		// Output Filename field + presets
		ui.Column(c).Gap(6).Children(func() {
			ui.Row(c).Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "Output Filename").FontSize(13).FontWeight(600).TextColor(theme.Ink)
				ui.Text(c, "(optional)").FontSize(11).TextColor(theme.InkMuted).Grow(1)
				ui.Row(c).Wrap().Gap(6).AlignItems(ui.Center).Children(func() {
					for _, preset := range FilenameTemplatePresets() {
						pr := preset
						bauhausToggleChip(c, pr.Label, p.state.CustomFilename == pr.Value, func() {
							p.state.CustomFilename = pr.Value
							redraw()
						})
					}
				})
			})
			fnInput := ui.TextInput(c, &p.state.CustomFilename).
				Placeholder("%(title)s").
				Label("Output Filename").
				Height(36).
				Radius(0).
				Padding(0, 12).
				Border(2, theme.Ink).
				Background(theme.Paper).
				TextColor(theme.Ink).
				FocusRingColor(theme.Ink).
				FillWidth()
			fnInput.OnChange(func() {
				redraw()
			})
		})
	})
}

func (p *Plugin) renderTrimPostTab(c *ui.Context, redraw func()) {
	ui.Column(c).Border(2, theme.Ink).Background(theme.Paper).Padding(16).Gap(14).Children(func() {
		// Time-range clipping
		ui.Column(c).Gap(6).Children(func() {
			ui.Text(c, "Time-Range Clipping").FontSize(13).FontWeight(600).TextColor(theme.Ink)
			ui.Row(c).Wrap().Gap(12).AlignItems(ui.Center).Children(func() {
				startIn := ui.TextInput(c, &p.state.TimeRangeStart).
					Placeholder("Start (e.g. 00:01:30)").
					Label("Clip Start").
					Height(36).MinWidth(120).Radius(0).Padding(0, 10).
					Border(2, theme.Ink).Background(theme.Paper).TextColor(theme.Ink).
					FocusRingColor(theme.Ink).Grow(1)
				startIn.OnChange(func() {
					p.state.TimeRangeError = ""
					redraw()
				})

				endIn := ui.TextInput(c, &p.state.TimeRangeEnd).
					Placeholder("End (e.g. 00:02:45)").
					Label("Clip End").
					Height(36).MinWidth(120).Radius(0).Padding(0, 10).
					Border(2, theme.Ink).Background(theme.Paper).TextColor(theme.Ink).
					FocusRingColor(theme.Ink).Grow(1)
				endIn.OnChange(func() {
					p.state.TimeRangeError = ""
					redraw()
				})

				bauhausToggleChip(c, "Keyframe Cuts", p.state.ForceKeyframesCuts, func() {
					p.state.ForceKeyframesCuts = !p.state.ForceKeyframesCuts
					redraw()
				})
			})
			if p.state.TimeRangeError != "" {
				ui.Text(c, p.state.TimeRangeError).FontSize(12).TextColor(theme.Danger)
			}
		})

		// SponsorBlock
		ui.Column(c).Gap(6).Children(func() {
			ui.Text(c, "SponsorBlock").FontSize(13).FontWeight(600).TextColor(theme.Ink)
			ui.Row(c).Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
				modes := []FormatOption{
					{Label: "Off", Value: ""},
					{Label: "Remove", Value: "remove"},
					{Label: "Mark Chapters", Value: "mark"},
				}
				for _, m := range modes {
					mode := m
					bauhausToggleChip(c, mode.Label, p.state.SponsorBlockMode == mode.Value, func() {
						p.state.SponsorBlockMode = mode.Value
						redraw()
					})
				}
			})
			if p.state.SponsorBlockMode != "" {
				ui.Row(c).Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
					for _, cat := range DefaultSponsorBlockCategories() {
						category := cat
						bauhausToggleChip(c, category, p.state.SponsorBlockCats[category], func() {
							p.state.SponsorBlockCats[category] = !p.state.SponsorBlockCats[category]
							redraw()
						})
					}
				})
			}
		})

		// Metadata & Subtitles
		ui.Column(c).Gap(6).Children(func() {
			ui.Text(c, "Metadata & Subtitles").FontSize(13).FontWeight(600).TextColor(theme.Ink)
			ui.Row(c).Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
				bauhausToggleChip(c, "Embed Subtitles", p.state.EmbedSubs, func() {
					p.state.EmbedSubs = !p.state.EmbedSubs
					redraw()
				})
				bauhausToggleChip(c, "Auto Subs", p.state.WriteAutoSubs, func() {
					p.state.WriteAutoSubs = !p.state.WriteAutoSubs
					redraw()
				})
				bauhausToggleChip(c, "Thumbnail", p.state.EmbedThumbnail, func() {
					p.state.EmbedThumbnail = !p.state.EmbedThumbnail
					redraw()
				})
				bauhausToggleChip(c, "Chapters", p.state.EmbedChapters, func() {
					p.state.EmbedChapters = !p.state.EmbedChapters
					redraw()
				})
				bauhausToggleChip(c, "Metadata", p.state.EmbedMetadata, func() {
					p.state.EmbedMetadata = !p.state.EmbedMetadata
					redraw()
				})
			})
			if p.state.EmbedSubs {
				subIn := ui.TextInput(c, &p.state.SubLangs).
					Placeholder("Subtitle languages (e.g. en,id)").
					Label("Subtitle Languages").
					Height(36).Radius(0).Padding(0, 10).
					Border(2, theme.Ink).Background(theme.Paper).TextColor(theme.Ink).
					FocusRingColor(theme.Ink).FillWidth()
				subIn.OnChange(func() { redraw() })
			}
		})
	})
}

func (p *Plugin) renderNetworkAuthTab(c *ui.Context, binPath string, redraw func()) {
	ui.Column(c).Border(2, theme.Ink).Background(theme.Paper).Padding(16).Gap(14).Children(func() {
		ui.Row(c).Wrap().Gap(12).AlignItems(ui.Start).Children(func() {
			ui.Column(c).MinWidth(170).Grow(1).Gap(6).Children(func() {
				ui.Text(c, "Browser Cookies").FontSize(13).FontWeight(600).TextColor(theme.Ink)
				browsers := []string{"none", "chrome", "firefox", "safari", "edge", "brave"}
				sel := ui.Select(c, &p.state.CookiesFromBrowser, browsers).
					Height(36).MinWidth(0).FillWidth().Padding(0, 10).Border(2, theme.Ink).Background(theme.Paper)
				sel.OnChange(func() { redraw() })
			})

			ui.Column(c).MinWidth(170).Grow(1).Gap(6).Children(func() {
				ui.Text(c, "Concurrent Fragments").FontSize(13).FontWeight(600).TextColor(theme.Ink)
				frags := []string{"1", "2", "4", "8"}
				sel := ui.Select(c, &p.state.ConcurrentFragments, frags).
					Height(36).MinWidth(0).FillWidth().Padding(0, 10).Border(2, theme.Ink).Background(theme.Paper)
				sel.OnChange(func() { redraw() })
			})
		})

		ui.Row(c).Wrap().Gap(12).AlignItems(ui.Start).Children(func() {
			ui.Column(c).MinWidth(170).Grow(1).Gap(6).Children(func() {
				ui.Text(c, "Proxy URL").FontSize(13).FontWeight(600).TextColor(theme.Ink)
				proxyIn := ui.TextInput(c, &p.state.ProxyURL).
					Placeholder("socks5://127.0.0.1:1080").
					Label("Proxy URL").
					Height(36).Radius(0).Padding(0, 10).
					Border(2, theme.Ink).Background(theme.Paper).TextColor(theme.Ink).
					FocusRingColor(theme.Ink).FillWidth()
				proxyIn.OnChange(func() {
					p.state.NetworkError = ""
					redraw()
				})
			})

			ui.Column(c).MinWidth(170).Grow(1).Gap(6).Children(func() {
				ui.Text(c, "Rate Limit").FontSize(13).FontWeight(600).TextColor(theme.Ink)
				rateIn := ui.TextInput(c, &p.state.RateLimit).
					Placeholder("e.g. 5M or 500K").
					Label("Rate Limit").
					Height(36).Radius(0).Padding(0, 10).
					Border(2, theme.Ink).Background(theme.Paper).TextColor(theme.Ink).
					FocusRingColor(theme.Ink).FillWidth()
				rateIn.OnChange(func() {
					p.state.NetworkError = ""
					redraw()
				})
			})
		})

		// Cookies file picker & Update yt-dlp
		ui.Row(c).Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
			cookiesDisplay := "No cookies.txt selected"
			if p.state.CookiesFile != "" {
				cookiesDisplay = filepath.Base(p.state.CookiesFile)
			}
			ui.Text(c, cookiesDisplay).FontSize(12).TextColor(theme.InkMuted).MinWidth(140).Grow(1)

			components.SecondaryButton(c, "Cookies File...", func() {
				go func() {
					paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
						Filters: []mygo.FileFilter{{Name: "Cookies", Extensions: []string{"txt"}}},
					})
					if err == nil && len(paths) > 0 {
						p.state.CookiesFile = paths[0]
						p.state.NetworkError = ""
						redraw()
					}
				}()
			})
			if p.state.CookiesFile != "" {
				components.SecondaryButton(c, "Clear Cookies", func() {
					p.state.CookiesFile = ""
					redraw()
				})
			}

			updateLabel := "Update yt-dlp"
			if p.state.IsUpdatingBin {
				updateLabel = "Updating..."
			}
			components.SecondaryButton(c, updateLabel, func() {
				if p.state.IsUpdatingBin {
					return
				}
				p.state.IsUpdatingBin = true
				redraw()
				go func() {
					msg, err := UpdateBinary(context.Background(), binPath)
					p.state.IsUpdatingBin = false
					if err != nil {
						p.state.NetworkError = err.Error()
					} else {
						p.state.ActionMessage = msg
					}
					redraw()
				}()
			})
		})

		if p.state.NetworkError != "" {
			ui.Text(c, p.state.NetworkError).FontSize(12).TextColor(theme.Danger)
		}
	})
}

func (p *Plugin) renderPlaylistCard(c *ui.Context, binPath string, redraw func()) {
	pm := p.state.PlaylistMetadata
	if pm == nil {
		return
	}
	selectedCount := 0
	for _, e := range pm.Entries {
		if p.state.SelectedEntries[e.URL] {
			selectedCount++
		}
	}

	ui.Column(c).Border(2, theme.Ink).Background(theme.Ground).Padding(16).Gap(12).Children(func() {
		ui.Row(c).Wrap().Justify(ui.SpaceBetween).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Column(c).MinWidth(160).Gap(2).Children(func() {
				ui.Text(c, pm.Title).FontSize(14).FontWeight(700).TextColor(theme.Ink)
				ui.Text(c, fmt.Sprintf("%d items · %d selected", len(pm.Entries), selectedCount)).
					FontSize(12).TextColor(theme.InkMuted)
			})
			ui.Row(c).Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
				components.SecondaryButton(c, "Select All", func() {
					for _, e := range pm.Entries {
						p.state.SelectedEntries[e.URL] = true
					}
					redraw()
				})
				components.SecondaryButton(c, "Clear", func() {
					p.state.SelectedEntries = make(map[string]bool)
					redraw()
				})
				components.SecondaryButton(c, "Dismiss", func() {
					p.state.PlaylistMetadata = nil
					p.state.SelectedEntries = make(map[string]bool)
					p.state.PlaylistList = ui.ListState{}
					redraw()
				})
			})
		})

		listH := float32(len(pm.Entries) * 44)
		if listH > 220 {
			listH = 220
		}
		if listH < 88 {
			listH = 88
		}
		entries := pm.Entries
		p.state.PlaylistList.Key = func(i int) any { return entries[i].URL }
		p.state.PlaylistList.Label = func(i int) string { return entries[i].Title }
		ui.List(c, &p.state.PlaylistList, len(entries), func(i int) {
			entry := entries[i]
			checked := p.state.SelectedEntries[entry.URL]
			ui.Row(c).FillWidth().Padding(6, 8).Gap(10).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(theme.BorderQuiet).Children(func() {
				prefix := "[ ]"
				if checked {
					prefix = "[✓]"
				}
				bauhausToggleChip(c, fmt.Sprintf("%s #%d", prefix, entry.Index), checked, func() {
					p.state.SelectedEntries[entry.URL] = !checked
					redraw()
				})
				ui.Text(c, entry.Title).FontSize(12).TextColor(theme.Ink).Grow(1).SingleLine()
				if entry.DurationString != "" {
					ui.Text(c, entry.DurationString).FontSize(11).TextColor(theme.InkMuted).SingleLine()
				}
			})
		}).FillWidth().Height(listH).Border(1, theme.Ink).Background(theme.Paper)

		if selectedCount > 0 {
			components.PrimaryButton(c, fmt.Sprintf("Download Selected (%d)", selectedCount), func() {
				p.startPlaylistDownload(binPath, redraw)
			})
		}
	})
}

func mapOptions(opts []FormatOption) ([]string, map[string]string, map[string]string) {
	labels := make([]string, 0, len(opts))
	valToLabel := make(map[string]string)
	labelToVal := make(map[string]string)
	for _, opt := range opts {
		labels = append(labels, opt.Label)
		valToLabel[opt.Value] = opt.Label
		labelToVal[opt.Label] = opt.Value
	}
	return labels, valToLabel, labelToVal
}

func formatViews(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.1fB views", float64(n)/1_000_000_000)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM views", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK views", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d views", n)
	}
}
