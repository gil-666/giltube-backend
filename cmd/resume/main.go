package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// QualityStatus represents the encoding status of a single quality
type QualityStatus struct {
	Name      string
	Path      string
	Encoded   bool
	Segments  int
	PlaylistValid bool
}

// parsePlaylist reads the .m3u8 file and returns the expected segment count
func parsePlaylist(playlistPath string) (int, bool, error) {
	file, err := os.Open(playlistPath)
	if err != nil {
		return 0, false, err
	}
	defer file.Close()

	var segmentCount int
	var hasEndTag bool
	
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		
		// Check for end tag (indicates encoding completed)
		if line == "#EXT-X-ENDLIST" {
			hasEndTag = true
		}
		
		// Count segment files (lines that end with .ts)
		if strings.HasSuffix(line, ".ts") {
			segmentCount++
		}
	}
	
	if err := scanner.Err(); err != nil {
		return 0, false, err
	}
	
	return segmentCount, hasEndTag, nil
}

// CheckEncodingStatus examines the output directory and reports which qualities are encoded
func CheckEncodingStatus(outputDir string) {
	qualities := []string{"144p", "240p", "360p", "480p", "720p", "1080p", "2160p"}
	
	fmt.Printf("Checking encoding status for: %s\n", outputDir)
	fmt.Println(strings.Repeat("=", 80))
	
	var encoded []string
	var incomplete []string
	var missing []string
	
	for _, quality := range qualities {
		qualityDir := filepath.Join(outputDir, quality)
		playlistPath := filepath.Join(qualityDir, "playlist.m3u8")
		
		// Check if quality directory exists
		if _, err := os.Stat(qualityDir); os.IsNotExist(err) {
			missing = append(missing, quality)
			fmt.Printf("❌ %-10s NOT STARTED (directory missing)\n", quality)
			continue
		}
		
		// Check if playlist exists
		if _, err := os.Stat(playlistPath); err != nil {
			missing = append(missing, quality)
			fmt.Printf("❌ %-10s INCOMPLETE (no playlist.m3u8)\n", quality)
			continue
		}
		
		// Parse playlist to check if encoding completed
		expectedSegments, hasEndTag, err := parsePlaylist(playlistPath)
		if err != nil {
			missing = append(missing, quality)
			fmt.Printf("❌ %-10s ERROR reading playlist: %v\n", quality, err)
			continue
		}
		
		// Count actual segments on disk
		segmentPattern := filepath.Join(qualityDir, "segment_*.ts")
		matches, err := filepath.Glob(segmentPattern)
		if err != nil || len(matches) == 0 {
			missing = append(missing, quality)
			fmt.Printf("❌ %-10s INCOMPLETE (no segments on disk)\n", quality)
			continue
		}
		
		// Check if encoding is complete
		if !hasEndTag {
			incomplete = append(incomplete, quality)
			fmt.Printf("⚠️  %-10s INCOMPLETE (missing #EXT-X-ENDLIST, has %d segments, expects %d)\n", 
				quality, len(matches), expectedSegments)
			continue
		}
		
		// Verify segment count matches playlist
		if len(matches) < expectedSegments {
			incomplete = append(incomplete, quality)
			fmt.Printf("⚠️  %-10s INCOMPLETE (has %d segments, playlist expects %d)\n", 
				quality, len(matches), expectedSegments)
			continue
		}
		
		encoded = append(encoded, quality)
		fmt.Printf("✓ %-10s COMPLETE (%d segments, finalized)\n", quality, len(matches))
	}
	
	fmt.Println(strings.Repeat("=", 80))
	
	if len(encoded) == 0 && len(incomplete) == 0 {
		fmt.Println("⚠️  No qualities encoded - encode never started or was deleted")
	} else if len(encoded) == len(qualities) {
		fmt.Println("✓ All qualities are complete - encoding is finished!")
	} else {
		fmt.Printf("\n📊 RESUME STATUS:\n")
		fmt.Printf("   ✓ Complete:   %v\n", encoded)
		if len(incomplete) > 0 {
			fmt.Printf("   ⚠️  Incomplete: %v (need to re-encode)\n", incomplete)
		}
		if len(missing) > 0 {
			fmt.Printf("   ❌ Missing:   %v (need to encode)\n", missing)
		}
		fmt.Printf("\nTo resume encoding, simply restart the encoder.\n")
		fmt.Printf("The encoder will automatically skip %v and encode %v.\n", 
			encoded, append(incomplete, missing...))
	}
	
	// Check for other files
	fmt.Printf("\n📁 Other files in output directory:\n")
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		fmt.Printf("Error reading directory: %v\n", err)
		return
	}
	
	for _, entry := range entries {
		if !entry.IsDir() {
			info, _ := entry.Info()
			fmt.Printf("   - %s (%d bytes)\n", entry.Name(), info.Size())
		}
	}
}

func main() {
	flag.Parse()
	
	if flag.NArg() < 1 {
		fmt.Println("Usage: resume <output-directory>")
		fmt.Println("")
		fmt.Println("Examples:")
		fmt.Println("  resume /home/gil/giltube/output/db180c4c-b18a-409b-bed5-751e34a16036")
		fmt.Println("  resume /output/db180c4c-b18a-409b-bed5-751e34a16036")
		fmt.Println("")
		fmt.Println("This tool checks which video qualities have been encoded and which still need encoding.")
		os.Exit(1)
	}
	
	outputDir := flag.Arg(0)
	
	// Verify directory exists
	if _, err := os.Stat(outputDir); os.IsNotExist(err) {
		fmt.Printf("Error: Directory not found: %s\n", outputDir)
		os.Exit(1)
	}
	
	CheckEncodingStatus(outputDir)
}
