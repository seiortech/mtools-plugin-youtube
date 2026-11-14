package youtube

import (
	"context"
	"embed"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/seiortech/mtools-sdk/ui/i18n"
	"github.com/seiortech/mtools-sdk/job"
	plugin "github.com/seiortech/mtools-sdk/settings"
	"github.com/seiortech/mtools-sdk/ui/tool"
)

//go:embed locales/*.json
var localesFS embed.FS

const (
	PluginID tool.PluginID = "com.mtools.youtube"
	ToolID   tool.ToolID   = "youtube"
)

// PluginSettingsSchema returns declared settings schema for YouTube Downloader.
func PluginSettingsSchema() []plugin.SettingField {
	return []plugin.SettingField{
		{
			Key:         "binary_path",
			Type:        plugin.SettingFile,
			Label:       "yt-dlp executable path",
			Description: "Custom yt-dlp path. Leave empty to use auto-downloaded binary.",
			Default:     "",
			Required:    false,
			Placeholder: "/usr/local/bin/yt-dlp",
			Filter: &plugin.SettingFileFilter{
				Name:       "Executable",
				Extensions: []string{"", "exe"},
			},
			Action: &plugin.SettingAction{
				ID:          "fetch_ytdlp",
				Label:       "Download yt-dlp",
				Description: "Download official standalone yt-dlp binary to plugin data directory.",
			},
		},
		{
			Key:         "download_dir",
			Type:        plugin.SettingDirectory,
			Label:       "Download folder",
			Description: "Destination folder for finished media files.",
			Default:     "~/Downloads",
			Required:    true,
		},
		{
			Key:         "quality",
			Type:        plugin.SettingDropdown,
			Label:       "Preferred quality",
			Description: "Target video resolution when stream is available.",
			Default:     "1080",
			Options: []plugin.SettingOption{
				{Label: "Best available", Value: "best"},
				{Label: "1080p", Value: "1080"},
				{Label: "720p", Value: "720"},
				{Label: "Audio only (MP3)", Value: "audio"},
			},
		},
		{
			Key:         "embed_subtitles",
			Type:        plugin.SettingToggle,
			Label:       "Embed subtitles",
			Description: "Download and embed available subtitles into output container.",
			Default:     false,
		},
		{
			Key:         "ffmpeg_path",
			Type:        plugin.SettingFile,
			Label:       "ffmpeg executable path",
			Description: "Path to ffmpeg executable for merging, clipping, audio conversion, and metadata embedding.",
			Default:     "",
			Required:    false,
			Placeholder: "/usr/local/bin/ffmpeg",
			Filter: &plugin.SettingFileFilter{
				Name:       "Executable",
				Extensions: []string{"", "exe"},
			},
			Action: &plugin.SettingAction{
				ID:          "fetch_ffmpeg",
				Label:       "Download ffmpeg",
				Description: "Auto-download the official standalone ffmpeg release for your platform.",
			},
		},
		{
			Key:         "cookies_from_browser",
			Type:        plugin.SettingDropdown,
			Label:       "Browser cookies",
			Description: "Read authentication cookies from a local browser for age-restricted or member videos.",
			Default:     "none",
			Options: []plugin.SettingOption{
				{Label: "None", Value: "none"},
				{Label: "Chrome", Value: "chrome"},
				{Label: "Firefox", Value: "firefox"},
				{Label: "Safari", Value: "safari"},
				{Label: "Edge", Value: "edge"},
				{Label: "Brave", Value: "brave"},
			},
		},
		{
			Key:         "proxy_url",
			Type:        plugin.SettingText,
			Label:       "Proxy URL",
			Description: "Optional HTTP, HTTPS, or SOCKS5 proxy URL.",
			Default:     "",
			Placeholder: "socks5://127.0.0.1:1080",
		},
		{
			Key:         "rate_limit",
			Type:        plugin.SettingText,
			Label:       "Speed limit",
			Description: "Maximum download rate (e.g. 5M, 500K). Leave empty for unlimited.",
			Default:     "",
			Placeholder: "5M",
		},
		{
			Key:         "concurrent_fragments",
			Type:        plugin.SettingDropdown,
			Label:       "Concurrent fragments",
			Description: "Number of DASH/HLS fragments to download in parallel.",
			Default:     "1",
			Options: []plugin.SettingOption{
				{Label: "1 (Default)", Value: "1"},
				{Label: "2", Value: "2"},
				{Label: "4", Value: "4"},
				{Label: "8", Value: "8"},
			},
		},
	}
}

// Plugin implements the native YouTube Downloader tool.
type Plugin struct {
	tool.PluginPaths
	store          plugin.SettingsStore
	queue          *job.Queue
	state          *ViewState
	history        *HistoryStore
	catalog        *i18n.Catalog
	langResolver   func() i18n.Lang
	externalRunner func(j DownloadJob) (job.Job, bool)
}

// LocaleTranslator implements tool.LocaleProvider.
func (p *Plugin) LocaleTranslator() i18n.Translator {
	return p.catalog
}

// SetLanguageResolver implements tool.LocaleProvider.
func (p *Plugin) SetLanguageResolver(fn func() i18n.Lang) {
	p.langResolver = fn
}

func (p *Plugin) effectiveLang() i18n.Lang {
	if p.store != nil {
		if raw := p.store.GetString("language"); raw != "" && raw != "global" {
			return i18n.NormalizeLang(raw)
		}
	}
	if p.langResolver != nil {
		return p.langResolver()
	}
	return i18n.CurrentLang()
}

func (p *Plugin) t(key string) string {
	if p.catalog == nil {
		return key
	}
	return p.catalog.T(p.effectiveLang(), key)
}

// SetExternalRunner configures a runner that delegates YouTube downloads to an external bundle.
func (p *Plugin) SetExternalRunner(runner func(j DownloadJob) (job.Job, bool)) {
	p.externalRunner = runner
}

func (p *Plugin) enqueueJob(title string, j DownloadJob) job.JobID {
	if p.queue == nil {
		return ""
	}
	if p.externalRunner != nil {
		if extJob, ok := p.externalRunner(j); ok && extJob != nil {
			return p.queue.Enqueue(title, extJob)
		}
	}
	return p.queue.Enqueue(title, j)
}

// NewPlugin creates a YouTube downloader plugin instance.
func NewPlugin(dataDir string, store plugin.SettingsStore, queue *job.Queue) *Plugin {
	paths := tool.NewPluginPaths(dataDir)
	if store == nil {
		store, _ = plugin.NewJSONSettingsStore(paths.GetSettingsPath(), PluginSettingsSchema())
	}
	cat, err := i18n.NewCatalogFromFS(localesFS, "locales")
	if err != nil {
		cat = i18n.NewCatalog()
	}
	if dataDir != "" {
		_ = cat.LoadDir(filepath.Join(dataDir, "i18n"))
	}
	return &Plugin{
		PluginPaths: paths,
		store:       store,
		queue:       queue,
		state:       NewViewState(),
		history:     NewHistoryStore(dataDir),
		catalog:     cat,
	}
}

// History returns the plugin history store.
func (p *Plugin) History() *HistoryStore {
	if p.history == nil {
		p.history = NewHistoryStore(p.GetDefaultPath())
	}
	return p.history
}

// HistoryEntries implements tool.HistoryProvider for global history.
func (p *Plugin) HistoryEntries() []tool.HistoryEntry {
	items := p.History().All()
	entries := make([]tool.HistoryEntry, len(items))
	for i, it := range items {
		metaParts := make([]string, 0, 3)
		if it.Channel != "" {
			metaParts = append(metaParts, it.Channel)
		}
		if it.Duration != "" {
			metaParts = append(metaParts, it.Duration)
		}
		metaParts = append(metaParts, it.DownloadedAt.Format("15:04"))

		entries[i] = tool.HistoryEntry{
			ID:        it.ID,
			ToolID:    ToolID,
			ToolName:  p.t("yt.name"),
			Title:     it.Title,
			Subtitle:  strings.Join(metaParts, " · "),
			FilePath:  it.FilePath,
			Timestamp: it.DownloadedAt,
		}
	}
	return entries
}

// DeleteHistory removes an item by ID from the history store.
func (p *Plugin) DeleteHistory(id string) error {
	return p.History().Delete(id)
}

// ClearHistory removes all items from the history store.
func (p *Plugin) ClearHistory() error {
	return p.History().ClearAll()
}

// Meta returns tool metadata.
func (p *Plugin) Meta() tool.ToolMeta {
	return tool.ToolMeta{
		ID:          ToolID,
		PluginID:    PluginID,
		Name:        p.t("yt.name"),
		Category:    "Media",
		Description: p.t("yt.desc"),
		Icon:        "play",
	}
}

// Settings returns the settings store.
func (p *Plugin) Settings() plugin.SettingsStore {
	return p.store
}

// DefaultBinaryPath returns the default target path for yt-dlp in the plugin's data directory.
func (p *Plugin) DefaultBinaryPath() string {
	return p.GetBinaryPath(BinaryName())
}

// CheckDependencies inspects external binaries required by YouTube Downloader.
func (p *Plugin) CheckDependencies() []tool.DependencyStatus {
	resolved, err := ResolveBinary(p.store.GetString("binary_path"), p.GetDefaultPath())
	ffmpegResolved, ffmpegErr := ResolveFFmpeg(p.store.GetString("ffmpeg_path"), p.GetDefaultPath())
	return []tool.DependencyStatus{
		{
			ID:          "yt-dlp",
			Name:        "yt-dlp executable",
			Required:    true,
			Found:       err == nil,
			Path:        resolved,
			DefaultPath: p.DefaultBinaryPath(),
		},
		{
			ID:          "ffmpeg",
			Name:        "ffmpeg executable",
			Required:    false,
			Found:       ffmpegErr == nil,
			Path:        ffmpegResolved,
			DefaultPath: p.GetBinaryPath(FFmpegBinaryName()),
		},
	}
}

// DefaultFFmpegPath returns the default data-directory target path for ffmpeg.
func (p *Plugin) DefaultFFmpegPath() string {
	return p.GetBinaryPath(FFmpegBinaryName())
}

// InstallDependencyToDefault downloads helper binary into the plugin's default data folder.
func (p *Plugin) InstallDependencyToDefault(ctx context.Context, depID string, progress func(done float64, msg string)) (string, error) {
	var prCallback func(job.Progress)
	if progress != nil {
		prCallback = func(pr job.Progress) {
			progress(pr.Done, pr.Message)
		}
	}
	switch depID {
	case "yt-dlp", "fetch_ytdlp":
		return p.DownloadBinaryWithProgress(ctx, p.DefaultBinaryPath(), prCallback)
	case "ffmpeg", "fetch_ffmpeg":
		return p.DownloadFFmpegWithProgress(ctx, p.DefaultFFmpegPath(), prCallback)
	default:
		return "", fmt.Errorf("unknown dependency: %s", depID)
	}
}

// DownloadBinaryWithProgress downloads the yt-dlp binary to targetPath with live progress.
func (p *Plugin) DownloadBinaryWithProgress(ctx context.Context, targetPath string, progress func(job.Progress)) (string, error) {
	if targetPath == "" {
		targetPath = p.DefaultBinaryPath()
	}
	target, err := DownloadBinaryTo(ctx, targetPath, progress)
	if err != nil {
		return "", err
	}
	p.store.Set("binary_path", target)
	return target, p.store.Save()
}

// DownloadFFmpegWithProgress downloads the ffmpeg binary to targetPath with live progress.
func (p *Plugin) DownloadFFmpegWithProgress(ctx context.Context, targetPath string, progress func(job.Progress)) (string, error) {
	if targetPath == "" {
		targetPath = p.DefaultFFmpegPath()
	}
	target, err := DownloadFFmpegTo(ctx, targetPath, progress)
	if err != nil {
		return "", err
	}
	p.store.Set("ffmpeg_path", target)
	return target, p.store.Save()
}

// RunAction executes plugin-level action buttons from settings or tool UI.
func (p *Plugin) RunAction(ctx context.Context, actionID string) error {
	switch actionID {
	case "fetch_ytdlp":
		_, err := p.DownloadBinaryWithProgress(ctx, "", nil)
		return err
	case "fetch_ffmpeg":
		_, err := p.DownloadFFmpegWithProgress(ctx, "", nil)
		return err
	case "update_ytdlp":
		bin, err := ResolveBinary(p.store.GetString("binary_path"), p.GetDefaultPath())
		if err != nil {
			return err
		}
		_, err = UpdateBinary(ctx, bin)
		return err
	default:
		return fmt.Errorf("unknown action: %s", actionID)
	}
}

// View mounts the native workspace UI.
func (p *Plugin) View(c *ui.Context) {
	p.RenderWorkspace(c)
}

// OnOpen implements tool.WorkspaceLifecycle.
func (p *Plugin) OnOpen() {
	if p.state == nil {
		p.state = NewViewState()
	}
	_ = p.EnsureDirs()
}

// HasActiveJob implements tool.WorkspaceLifecycle.
func (p *Plugin) HasActiveJob() bool {
	if p.state == nil {
		return false
	}
	if p.state.IsLoadingPreview {
		return true
	}
	if p.queue == nil {
		return false
	}
	if p.state.ActiveJobID != "" {
		if rec, ok := p.queue.GetRecord(p.state.ActiveJobID); ok && (rec.Status == job.StatusQueued || rec.Status == job.StatusRunning) {
			return true
		}
	}
	for _, id := range p.state.BatchJobIDs {
		if rec, ok := p.queue.GetRecord(id); ok && (rec.Status == job.StatusQueued || rec.Status == job.StatusRunning) {
			return true
		}
	}
	return false
}

// OnClose implements tool.WorkspaceLifecycle.
func (p *Plugin) OnClose() {
	if p.state != nil {
		p.state.cancelPreview()
		if p.queue != nil {
			if p.state.ActiveJobID != "" {
				p.queue.Cancel(p.state.ActiveJobID)
			}
			for _, id := range p.state.BatchJobIDs {
				p.queue.Cancel(id)
			}
		}
	}
	p.state = NewViewState()
	_ = p.CleanTemp()
}
