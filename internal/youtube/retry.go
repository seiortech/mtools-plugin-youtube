package youtube

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/seiortech/mtools-sdk/pluginapi"
)

// ErrorKind classifies yt-dlp or download failures for retry and auto-continue decisions.
type ErrorKind uint8

const (
	ErrKindPermanent ErrorKind = iota
	ErrKindRetryable
	ErrKindCanceled
)

// RetryPolicy configures automatic retry behavior for transient download failures.
type RetryPolicy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

// DefaultRetryPolicy returns a balanced retry policy for transient network/rate-limit errors.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts: 3,
		BaseDelay:   2 * time.Second,
		MaxDelay:    15 * time.Second,
	}
}

func (p RetryPolicy) backoff(attempt int) time.Duration {
	delay := p.BaseDelay << attempt
	if p.MaxDelay > 0 && delay > p.MaxDelay {
		return p.MaxDelay
	}
	return delay
}

// ExtractLastError extracts the most relevant error line from yt-dlp stderr,
// preferring the final "ERROR:" line over earlier warnings.
func ExtractLastError(stderr string) string {
	lines := strings.Split(stderr, "\n")
	var lastErr string
	var lastNonEmpty string
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		lastNonEmpty = line
		if strings.HasPrefix(line, "ERROR:") || strings.Contains(line, " ERROR: ") {
			lastErr = line
		}
	}
	if lastErr != "" {
		return stringsTrim(lastErr)
	}
	return stringsTrim(lastNonEmpty)
}

// ClassifyError inspects an error and stderr output to determine whether it is
// canceled, retryable (transient), or permanent, and returns a concise user-facing message.
func ClassifyError(err error, stderr string) (ErrorKind, string) {
	if err == nil {
		return ErrKindPermanent, ""
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ErrKindCanceled, "Download canceled"
	}
	if errors.Is(err, ErrMissingBinary) ||
		errors.Is(err, ErrMissingFFmpeg) ||
		errors.Is(err, ErrInvalidURL) ||
		errors.Is(err, ErrInvalidTimeRange) ||
		errors.Is(err, ErrInvalidProxyURL) ||
		errors.Is(err, ErrInvalidCookiesFile) ||
		errors.Is(err, ErrInvalidRateLimit) {
		return ErrKindPermanent, err.Error()
	}

	summary := ExtractLastError(stderr)
	if summary == "" {
		summary = err.Error()
	}
	lower := strings.ToLower(stderr + "\n" + err.Error())

	// Permanent / user-action errors: do not retry.
	switch {
	case strings.Contains(lower, "sign in to confirm you're not a bot") ||
		strings.Contains(lower, "sign in to confirm your age") ||
		strings.Contains(lower, "login_required") ||
		strings.Contains(lower, "cookies"):
		return ErrKindPermanent, "Authentication required: select Browser Cookies or a Cookies File in Network & Auth (" + summary + ")"
	case strings.Contains(lower, "private video"):
		return ErrKindPermanent, "Video is private or requires access permissions"
	case strings.Contains(lower, "video unavailable") ||
		strings.Contains(lower, "this video has been removed") ||
		strings.Contains(lower, "account associated with this video has been terminated") ||
		strings.Contains(lower, "copyright"):
		return ErrKindPermanent, "Video unavailable: " + summary
	case strings.Contains(lower, "requested format is not available") ||
		strings.Contains(lower, "no video formats found"):
		return ErrKindPermanent, "Requested stream format is unavailable for this video"
	case strings.Contains(lower, "ffmpeg is not installed") ||
		strings.Contains(lower, "ffprobe and ffmpeg not found"):
		return ErrKindPermanent, ErrMissingFFmpeg.Error()
	case strings.Contains(lower, "no space left on device") ||
		strings.Contains(lower, "permission denied"):
		return ErrKindPermanent, "Filesystem error: " + summary
	}

	// Transient / network errors: safe to retry with --continue.
	retryablePatterns := []string{
		"http error 403",
		"http error 429",
		"http error 500",
		"http error 502",
		"http error 503",
		"http error 504",
		"timed out",
		"timeout",
		"connection reset",
		"connection refused",
		"temporary failure in name resolution",
		"network is unreachable",
		"unable to download webpage",
		"unable to extract",
		"fragment",
		"eof",
		"tls",
		"broken pipe",
		"giving up after",
		"did not get any data blocks",
	}
	for _, pat := range retryablePatterns {
		if strings.Contains(lower, pat) {
			return ErrKindRetryable, summary
		}
	}

	return ErrKindPermanent, summary
}

// RunWithRetry executes DownloadJob with exponential backoff on transient failures,
// preserving partial files between attempts so yt-dlp --continue resumes seamlessly.
func (j DownloadJob) RunWithRetry(ctx context.Context, policy RetryPolicy, progress func(pluginapi.Progress)) (pluginapi.RunResult, error) {
	if policy.MaxAttempts <= 0 {
		policy.MaxAttempts = 1
	}

	var lastErr error
	for attempt := 0; attempt < policy.MaxAttempts; attempt++ {
		if ctx.Err() != nil {
			return pluginapi.RunResult{}, context.Canceled
		}

		res, err := j.Run(ctx, progress)
		if err == nil {
			return res, nil
		}
		lastErr = err

		kind, msg := ClassifyError(err, err.Error())
		if kind != ErrKindRetryable || attempt+1 >= policy.MaxAttempts {
			if kind == ErrKindCanceled {
				return pluginapi.RunResult{}, context.Canceled
			}
			return pluginapi.RunResult{}, fmt.Errorf("%s", msg)
		}

		delay := policy.backoff(attempt)
		if progress != nil {
			progress(pluginapi.Progress{
				Done:    0,
				Total:   100,
				Unit:    "%",
				Message: fmt.Sprintf("Transient error — retrying in %s (attempt %d/%d)...", delay, attempt+2, policy.MaxAttempts),
				LogLine: fmt.Sprintf("[retry] attempt %d/%d failed (%s); waiting %s before resuming...", attempt+1, policy.MaxAttempts, msg, delay),
			})
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return pluginapi.RunResult{}, context.Canceled
		case <-timer.C:
		}
	}

	return pluginapi.RunResult{}, lastErr
}
