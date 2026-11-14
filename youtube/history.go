package youtube

import (
	"path/filepath"
	"time"

	"github.com/seiortech/mtools-sdk/job"
)

// HistoryItem records metadata of a completed download.
type HistoryItem struct {
	ID           string    `json:"id"`
	URL          string    `json:"url"`
	Title        string    `json:"title"`
	Channel      string    `json:"channel"`
	Duration     string    `json:"duration"`
	FilePath     string    `json:"file_path"`
	DownloadedAt time.Time `json:"downloaded_at"`
}

// GetID implements job.HistoryRecord.
func (h HistoryItem) GetID() string {
	return h.ID
}

// HistoryStore persists download history to JSON.
type HistoryStore = job.HistoryStore[HistoryItem]

// NewHistoryStore loads or initializes history from plugin data directory.
func NewHistoryStore(dataDir string) *HistoryStore {
	return job.NewHistoryStore[HistoryItem](filepath.Join(dataDir, "history.json"))
}
