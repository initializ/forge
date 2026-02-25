package brain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
)

// DownloadProgress reports download progress to a callback.
type DownloadProgress struct {
	TotalBytes      int64
	DownloadedBytes int64
	Resuming        bool
}

// ProgressFunc is called periodically during download.
type ProgressFunc func(DownloadProgress)

// ModelsDir returns the default directory for storing brain models.
// It is a variable to allow override in tests.
var ModelsDir = func() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".forge", "models")
	}
	return filepath.Join(home, ".forge", "models")
}

// ModelPath returns the full path to a model file in the models directory.
func ModelPath(filename string) string {
	return filepath.Join(ModelsDir(), filename)
}

// IsModelDownloaded checks if a model file exists at the expected path.
func IsModelDownloaded(filename string) bool {
	info, err := os.Stat(ModelPath(filename))
	return err == nil && info.Size() > 0
}

// DownloadModel downloads a model file with resume support and optional SHA256 verification.
func DownloadModel(model ModelInfo, progressFn ProgressFunc) error {
	dir := ModelsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create models dir: %w", err)
	}

	destPath := filepath.Join(dir, model.Filename)
	partPath := destPath + ".part"

	// Check for existing partial download
	var existingSize int64
	if info, err := os.Stat(partPath); err == nil {
		existingSize = info.Size()
	}

	// Create HTTP request with optional Range header for resume
	req, err := http.NewRequest("GET", model.URL, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	if existingSize > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", existingSize))
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Handle response status
	switch resp.StatusCode {
	case http.StatusOK:
		// Full download, reset existing size
		existingSize = 0
	case http.StatusPartialContent:
		// Resume accepted
	case http.StatusRequestedRangeNotSatisfiable:
		// File already complete or server doesn't support range
		existingSize = 0
		// Re-request without Range
		req.Header.Del("Range")
		_ = resp.Body.Close()
		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("download retry: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("unexpected status on retry: %s", resp.Status)
		}
	default:
		return fmt.Errorf("unexpected HTTP status: %s", resp.Status)
	}

	// Determine total size
	totalSize := model.Size
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		if n, err := strconv.ParseInt(cl, 10, 64); err == nil {
			totalSize = n + existingSize
		}
	}

	// Open part file for writing (append if resuming)
	flags := os.O_CREATE | os.O_WRONLY
	if existingSize > 0 {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	partFile, err := os.OpenFile(partPath, flags, 0o644)
	if err != nil {
		return fmt.Errorf("open part file: %w", err)
	}
	defer func() { _ = partFile.Close() }()

	// Download with progress reporting
	downloaded := existingSize
	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := partFile.Write(buf[:n]); writeErr != nil {
				return fmt.Errorf("write: %w", writeErr)
			}
			downloaded += int64(n)
			if progressFn != nil {
				progressFn(DownloadProgress{
					TotalBytes:      totalSize,
					DownloadedBytes: downloaded,
					Resuming:        existingSize > 0,
				})
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return fmt.Errorf("read: %w", readErr)
		}
	}

	if err := partFile.Close(); err != nil {
		return fmt.Errorf("close part file: %w", err)
	}

	// Verify SHA256 if provided
	if model.SHA256 != "" {
		hash, err := fileSHA256(partPath)
		if err != nil {
			return fmt.Errorf("compute sha256: %w", err)
		}
		if hash != model.SHA256 {
			_ = os.Remove(partPath)
			return fmt.Errorf("sha256 mismatch: expected %s, got %s", model.SHA256, hash)
		}
	}

	// Rename part file to final destination
	if err := os.Rename(partPath, destPath); err != nil {
		return fmt.Errorf("rename: %w", err)
	}

	return nil
}

// RemoveModel deletes a downloaded model file.
func RemoveModel(filename string) error {
	path := ModelPath(filename)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("model not found: %s", filename)
	}
	return os.Remove(path)
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
