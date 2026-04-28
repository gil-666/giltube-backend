package paths

import (
	"os"
	"path/filepath"
	"runtime"
)

var qualityPreference = []string{
	"2160p",
	"1080p",
	"720p",
	"480p",
	"360p",
	"240p",
	"144p",
}

func homeDir() string {
	if home := os.Getenv("HOME"); home != "" {
		return home
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

func OutputDir() string {
	if outputDir := os.Getenv("GILTUBE_OUTPUT_DIR"); outputDir != "" {
		return outputDir
	}

	if runtime.GOOS == "windows" {
		return "\\\\wsl.localhost\\Ubuntu\\home\\gil\\giltube\\output"
	}

	home := homeDir()
	if home == "" {
		return filepath.Join("giltube", "output")
	}
	return filepath.Join(home, "giltube", "output")
}

func DownloadsDir() string {
	if downloadsDir := os.Getenv("GILTUBE_DOWNLOADS_DIR"); downloadsDir != "" {
		return downloadsDir
	}

	if runtime.GOOS == "windows" {
		return "\\\\wsl.localhost\\Ubuntu\\home\\gil\\giltube\\downloads"
	}

	home := homeDir()
	if home == "" {
		return filepath.Join("giltube", "downloads")
	}
	return filepath.Join(home, "giltube", "downloads")
}

func VideoDir(videoID string) string {
	return filepath.Join(OutputDir(), videoID)
}

func VideoQualityDir(videoID, quality string) string {
	return filepath.Join(OutputDir(), videoID, quality)
}

func QualityPreference() []string {
	qualities := make([]string, len(qualityPreference))
	copy(qualities, qualityPreference)
	return qualities
}

func HighestAvailableQuality(videoID string) (string, bool) {
	for _, quality := range qualityPreference {
		playlistPath := filepath.Join(VideoQualityDir(videoID, quality), "playlist.m3u8")
		if info, err := os.Stat(playlistPath); err == nil && !info.IsDir() {
			return quality, true
		}
	}

	return "", false
}
