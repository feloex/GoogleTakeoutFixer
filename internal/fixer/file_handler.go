/*
GoogleTakeoutFixer - A tool to easily clean and organize Google Photos Takeout exports
Copyright (C) 2026 feloex

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package fixer

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Cache for diectory entries to prevent excessive disk reads (issue #5)
var (
	dirCache     = make(map[string][]os.DirEntry)
	dirCacheLock sync.RWMutex
)

// ReadDirCached returns cached directories or reads them it not present
func ReadDirCached(dir string) ([]os.DirEntry, error) {
	dirCacheLock.RLock()
	entries, ok := dirCache[dir]
	dirCacheLock.RUnlock()

	// Cache hit, return entries
	if ok {
		return entries, nil
	}

	dirCacheLock.Lock()
	defer dirCacheLock.Unlock()

	// Check again in case it was created while waiting for lock
	if entries, ok = dirCache[dir]; ok {
		return entries, nil
	}

	// Read directory and cache results
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	dirCache[dir] = entries

	return entries, nil
}

// ClearCache clears the directory cache for all paths
func ClearCache() {
	dirCacheLock.Lock()
	defer dirCacheLock.Unlock()
	// Reallocate map to clear everything
	dirCache = make(map[string][]os.DirEntry)
}

// ClearCacheDir clears the directory cache for a specific path
func ClearCacheDir(dir string) {
	dirCacheLock.Lock()
	defer dirCacheLock.Unlock()
	delete(dirCache, dir)
}

// All media extension to differ between media files and other files
// I could image that google does some renaming like with .mov (see issue #2)
var imageExtensions = []string{
	".jpg",
	".jpeg",
	".png",
	".gif",
	".webp",
	".bmp",
	".arw",
	".cr2",
	".cr3",
	".tif",
	".tiff",
	".heic",
	".heif",
	".dng",
	".raf",
	".rw2",
	".srw",
	".nef",
	".nrw",
	".orf",
}

var videoExtensions = []string{
	".mp4",
	".m4v",
	".mov",
	".mpeg",
	".m2ts",
	".webm",
	".wmv",
	".avi",
	".mkv",
	".3gp",
	".3g2",
	".mpg",
	".mts",
}

// Checks whether a file is a video file based on its extension
func IsVideoFile(path string) bool {
	extension := filepath.Ext(path)
	return slices.Contains(videoExtensions, strings.ToLower(extension))
}

// Duplicate a file from one path to another
func DuplicateFile(inputPath string, outputPath string) error {
	sourceFile, err := os.Open(inputPath)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destFile, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer destFile.Close()

	_, err = io.Copy(destFile, sourceFile)
	return err
}

// Discover directories within a path non recursively
func DiscoverDirs(path string) ([]os.DirEntry, error) {
	var dirList []os.DirEntry

	files, err := os.ReadDir(path)

	if err != nil {
		return nil, err
	}

	for _, file := range files {
		if file.IsDir() {
			dirList = append(dirList, file)
		}
	}

	return dirList, nil
}

// Find a matching sidecar JSON
func FindSidecar(imagePath string) (string, error) {
	// Scan directory non case sensitively for JSON sidecar files
	dir := filepath.Dir(imagePath)
	base := strings.TrimSuffix(filepath.Base(imagePath), filepath.Ext(imagePath))
	prefix := strings.ToLower(base)

	entries, err := ReadDirCached(dir)
	if err != nil {
		return "", err
	}

	for _, entry := range entries {
		name := entry.Name()
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, prefix) && strings.HasSuffix(lower, ".json") {
			return filepath.Join(dir, name), nil
		}
	}

	return "", nil
}

// Checks if the file at the given path has the specified extension
func IsNameExtension(extension string, path string) bool {
	return strings.EqualFold(filepath.Ext(path), extension)
}

// Checks whether a directory is a standart google year folder
func IsYearFolder(dirPath string) (bool, error) {
	// Year folder prefixes of some countries
	// yearPrefixes is mostly made by AI. I have not verified these, but i assume they are primarily correct.
	// Please create an issue if you find any mistakes or if you want to add more languages.
	yearPrefixes := []string{
		"Photos from ",     // English
		"Fotos von ",       // German
		"Photos de ",       // French
		"Foto del ",        // Italian
		"Fotos de ",        // Spanish / Portuguese
		"Foto's van ",      // Dutch
		"Zdjęcia z ",       // Polish
		"Фотографии из ",   // Russian
		"Foton från ",      // Swedish
		"Bilder fra ",      // Norwegian
		"Billeder fra ",    // Danish
		"Fotoğraflar ",     // Turkish
		"Fotografie z ",    // Czech
		"Fotók a ",         // Hungarian
		"Φωτογραφίες από ", // Greek
		"Fotografii din ",  // Romanian
		"Foto dari ",       // Indonesian
		"รูปภาพจาก ",       // Thai
		"Ảnh từ ",          // Vietnamese
	}

	for _, prefix := range yearPrefixes {
		if strings.HasPrefix(dirPath, prefix) {
			// The rest of the string has to be 4 characters long
			yearPart := strings.TrimPrefix(dirPath, prefix)
			if matched, _ := regexp.MatchString(`^\d{4}$`, yearPart); matched {
				return true, nil
			}
		}
	}
	return false, nil
}

// Checks whether a file, that is provided using its path, is a media file
func IsMediaFile(path string) bool {
	extension := strings.ToLower(filepath.Ext(path))
	isImage := slices.Contains(imageExtensions, extension)
	isVideo := slices.Contains(videoExtensions, extension)
	return isImage || isVideo
}

// Attempts to find an image file with the same base name as the video file
// This is used for live photos where the metadata is the images sidecar
// I think error handling could be improved here
func FindImagePartner(videoPath string) (string, error) {
	if !IsVideoFile(videoPath) {
		return "", nil
	}

	dir := filepath.Dir(videoPath)
	extension := filepath.Ext(videoPath)
	base := strings.TrimSuffix(filepath.Base(videoPath), extension)

	// Check all image extensions for a match
	for _, imgExt := range imageExtensions {
		candidate := filepath.Join(dir, base+imgExt)

		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}

		// Try uppercase extension
		candidateUpper := filepath.Join(dir, base+strings.ToUpper(imgExt))
		if _, err := os.Stat(candidateUpper); err == nil {
			return candidateUpper, nil
		}
	}

	return "", nil
}

// Counts all processable files in the source path
func CountProcessableFiles(sourcePath string) (int, error) {
	fileInfo, err := os.Stat(sourcePath)
	if err != nil {
		return 0, err
	}

	if !fileInfo.IsDir() {
		return 0, fmt.Errorf("source path is not a directory")
	}

	count := 0
	subdirs, err := DiscoverDirs(sourcePath)
	if err != nil {
		return 0, err
	}

	for _, dir := range subdirs {
		files, _ := os.ReadDir(filepath.Join(sourcePath, dir.Name()))
		for _, file := range files {
			if !file.IsDir() && IsMediaFile(file.Name()) {
				count++
			}
		}
	}

	if count == 0 {
		return 0, fmt.Errorf("no media files found in folder structure")
	}
	return count, nil
}

// Detect the month of a file based on its sidecar metadata
// Returns the month as an integer between 1 and 12
func DetectFileMonth(sourcePath string, sidecarPath string) (int, error) {
	if sidecarPath != "" {
		metadata, err := ReadJsonMetadata(sidecarPath)
		if err != nil {
			return 0, err
		}

		timestamp, err := strconv.ParseInt(metadata.PhotoTakenTime.Timestamp, 10, 64)
		if err != nil {
			return 0, err
		}

		return int(time.Unix(timestamp, 0).Month()), nil
	}

	fileInfo, err := os.Stat(sourcePath)
	if err != nil {
		return 0, err
	}

	return int(fileInfo.ModTime().Month()), nil
}

func ResolveOutputDir(
	fixerCtx *FixerContext,
	sourcePath string,
	sidecarPath string,
	sourceDirName string,
	isYearFolder bool,
) (string, error) {
	if fixerCtx.Options.Flatten {
		return fixerCtx.OutputRoot, nil
	}

	targetDir := fixerCtx.OutputRoot
	if sourceDirName != "" /*&& !fixerCtx.Options.IgnoreAlbums && !isYearFolder*/ {
		targetDir = filepath.Join(targetDir, sourceDirName)
	}

	if !fixerCtx.Options.MonthSubfolders {
		return targetDir, nil
	}

	month, err := DetectFileMonth(sourcePath, sidecarPath)
	if err != nil {
		return "", err
	}

	return filepath.Join(targetDir, strconv.Itoa(month)), nil
}
