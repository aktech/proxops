package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"
)

// Updater checks GitHub releases and replaces the running binary.
type Updater struct {
	repo      string
	logger    *slog.Logger
	lastCheck time.Time
	interval  time.Duration
}

// NewUpdater creates a self-updater.
func NewUpdater(repo string, interval time.Duration, logger *slog.Logger) *Updater {
	return &Updater{
		repo:     repo,
		logger:   logger,
		interval: interval,
	}
}

// githubRelease is the subset of GitHub API response we need.
type githubRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

// UpdateToVersion downloads and installs a specific version.
// Returns true if the binary was updated (caller should exit so systemd restarts).
func (u *Updater) UpdateToVersion(desired string) bool {
	if version == "dev" {
		u.logger.Debug("skipping self-update in dev mode")
		return false
	}

	desired = strings.TrimPrefix(desired, "v")
	if desired == version {
		return false
	}

	u.logger.Info("version mismatch, updating", "running", version, "desired", desired)

	release, err := u.fetchRelease("v" + desired)
	if err != nil {
		u.logger.Error("failed to fetch release", "version", desired, "error", err)
		return false
	}

	if err := u.downloadAndReplace(release); err != nil {
		u.logger.Error("self-update failed", "error", err)
		return false
	}

	u.logger.Info("updated to desired version, exiting for restart", "version", desired)
	return true
}

// CheckAndUpdateToLatest checks for a newer release and replaces the binary if found.
// Returns true if the binary was updated (caller should exit so systemd restarts).
func (u *Updater) CheckAndUpdateToLatest() bool {
	now := time.Now()
	if now.Sub(u.lastCheck) < u.interval {
		return false
	}
	u.lastCheck = now

	if version == "dev" {
		u.logger.Debug("skipping self-update in dev mode")
		return false
	}

	u.logger.Info("checking for updates", "current", version)

	release, err := u.fetchRelease("latest")
	if err != nil {
		u.logger.Warn("failed to check for updates", "error", err)
		return false
	}

	latestVersion := strings.TrimPrefix(release.TagName, "v")
	if latestVersion == version {
		u.logger.Info("already up to date", "version", version)
		return false
	}

	u.logger.Info("new version available", "current", version, "latest", latestVersion)

	if err := u.downloadAndReplace(release); err != nil {
		u.logger.Error("self-update failed", "error", err)
		return false
	}

	u.logger.Info("updated successfully, exiting for restart", "new_version", latestVersion)
	return true
}

func (u *Updater) fetchRelease(tagOrLatest string) (*githubRelease, error) {
	var url string
	if tagOrLatest == "latest" {
		url = fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", u.repo)
	} else {
		url = fmt.Sprintf("https://api.github.com/repos/%s/releases/tags/%s", u.repo, tagOrLatest)
	}
	client := &http.Client{Timeout: 15 * time.Second}

	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetch release: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned %d for %s", resp.StatusCode, tagOrLatest)
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("decode release: %w", err)
	}
	return &release, nil
}

func (u *Updater) downloadAndReplace(release *githubRelease) error {
	arch := runtime.GOARCH
	wantSuffix := fmt.Sprintf("linux_%s.tar.gz", arch)

	var downloadURL string
	for _, asset := range release.Assets {
		if strings.HasSuffix(asset.Name, wantSuffix) {
			downloadURL = asset.BrowserDownloadURL
			break
		}
	}
	if downloadURL == "" {
		return fmt.Errorf("no asset found for linux/%s", arch)
	}

	u.logger.Info("downloading update", "url", downloadURL)

	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get(downloadURL)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned %d", resp.StatusCode)
	}

	// Extract binary from tar.gz
	binaryData, err := extractBinaryFromTarGz(resp.Body, "proxpilot")
	if err != nil {
		return fmt.Errorf("extract: %w", err)
	}

	// Get the path to our own binary
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable path: %w", err)
	}

	// Write to a temp file next to the binary, then atomic rename
	tmpPath := execPath + ".new"
	if err := os.WriteFile(tmpPath, binaryData, 0755); err != nil {
		return fmt.Errorf("write temp binary: %w", err)
	}

	if err := os.Rename(tmpPath, execPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("replace binary: %w", err)
	}

	return nil
}

func extractBinaryFromTarGz(r io.Reader, name string) ([]byte, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tar: %w", err)
		}
		if hdr.Name == name {
			data, err := io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", name, err)
			}
			return data, nil
		}
	}
	return nil, fmt.Errorf("%s not found in archive", name)
}
