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
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Progress struct {
	Total     int
	Processed int
	Current   string
}

// TODO: Add more options
// TODO: Disable checkboxes when processing
type ProcessOptions struct {
	UseSymlinks         bool
	WriteMetadata       bool
	MonthSubfolders     bool
	IgnoreAlbums        bool
	Flatten             bool
	RestoreMOVExtension bool // See issue #2
	DryRun              bool
}

type FixerContext struct {
	Ctx        context.Context
	SourceRoot string
	OutputRoot string
	Options    ProcessOptions
	ProgressCh chan<- Progress
}

// Process is the main fixer entry point.
// Process
// -> DiscoverDirs
// --> ProcessDirectory
// ---> ProcessFile
// TODO: Do something in case files already exists instead of overwriting them
func Process(
	ctx context.Context,
	sourcePath string,
	outputPath string,
	progressCh chan<- Progress,
	options ProcessOptions,
) error {
	err := InitializeFileLogger()
	if err != nil {
		if LogHandler != nil {
			LogHandler(LoggerWarn, fmt.Sprintf("Failed to initialize file logger: %v", err))
		}
	} else {
		defer func() {
			err := CloseFileLogger()
			if err != nil && LogHandler != nil {
				LogHandler(LoggerWarn, fmt.Sprintf("Failed to close file logger: %v", err))
			}
		}()
	}

	Log(LoggerInfo, "Starting processing with source: %s and output: %s", sourcePath, outputPath)

	// Log total processing time when processing is done
	startTime := time.Now()
	defer func() {
		Log(LoggerInfo, "Total processing time: %s", time.Since(startTime).Round(time.Second))
		ClearCache()
	}()

	defer close(progressCh)
	p := Progress{}

	if ctx.Err() != nil {
		return ctx.Err()
	}

	if !options.DryRun && (options.WriteMetadata || options.RestoreMOVExtension) {
		if err := InitializeExifTool(); err != nil {
			Log(LoggerError, "Failed to initialize exiftool: %v", err)
			return err
		}
		defer CloseExifTool()
	}

	amountImages, err := CountProcessableFiles(sourcePath)
	if err != nil {
		Log(LoggerError, "Error counting images: %v", err)
		return err
	}
	p.Total = amountImages
	progressCh <- p

	fileInfo, err := os.Stat(sourcePath)
	if err != nil {
		Log(LoggerError, "Error getting file info: %v", err)
		return err
	}

	fixerCtx := &FixerContext{
		Ctx:        ctx,
		SourceRoot: sourcePath,
		OutputRoot: outputPath,
		Options:    options,
		ProgressCh: progressCh,
	}

	// process all directories in the source directory, ignore files in the source directory itself
	// because all media files should be inside of sub-folders
	if fileInfo.IsDir() {
		dirs, err := DiscoverDirs(sourcePath)
		if err != nil {
			Log(LoggerError, "Error discovering directories: %v", err)
		}

		for _, dir := range dirs {
			if ctx.Err() != nil {
				return ctx.Err()
			}

			dirPath := filepath.Join(sourcePath, dir.Name())
			var targetPath string = filepath.Join(outputPath, dir.Name())

			isYearFolder, err := IsYearFolder(dir.Name())
			if err != nil {
				Log(LoggerWarn, "Failed to determine if folder is a year folder for %s: %v", dir.Name(), err)
			}
			if (options.IgnoreAlbums || options.Flatten) && !isYearFolder {
				Log(LoggerInfo, "Skipping album folder: %s", dir.Name())
				continue
			}

			p = ProcessDirectory(fixerCtx, dirPath, targetPath, isYearFolder, p)
		}
	} else {
		err = ProcessFile(fixerCtx, sourcePath, "", false)
		if err != nil {
			Log(LoggerError, "Error processing file: %v", err)
		} else {
			p.Processed++
			p.Current = sourcePath
			progressCh <- p
		}
	}

	return nil
}

// Process a directory and fix all files within the directory. Ignores sub-directories.
func ProcessDirectory(
	fixerCtx *FixerContext,
	dirPath string,
	outputPath string,
	isYearFolder bool,
	p Progress,
) Progress {
	files, err := os.ReadDir(dirPath)
	if err != nil {
		Log(LoggerError, "Error reading directory: %v", err)
		return p
	}

	// TODO: Fix potential race conditions
	// Job pools
	// Buffered channel to avoid blocking
	jobs := make(chan string, len(files))
	completed := make(chan string)
	// Channel to capture errors
	errors := make(chan error)

	sourceDirName := filepath.Base(dirPath)

	var wg sync.WaitGroup
	workerCount := runtime.NumCPU() * 2 // x2 is faster for IO tasks, x more than that has no effect based on testing

	// Start worker goroutines
	for i := 0; i < workerCount; i++ {
		go func() {
			for imagePath := range jobs {
				if fixerCtx.Ctx.Err() != nil {
					wg.Done()
					continue
				}
				err := ProcessFile(fixerCtx, imagePath, sourceDirName, isYearFolder)
				if err != nil {
					errors <- fmt.Errorf("error processing file %s: %w", imagePath, err)
				} else {
					completed <- imagePath
				}
				wg.Done()
			}
		}()
	}

	// Send jobs directly, add work group before transmitting job
	for _, file := range files {
		if fixerCtx.Ctx.Err() != nil {
			break
		}
		if file.IsDir() {
			continue
		}

		imagePath := filepath.Join(dirPath, file.Name())

		// Check whether a file is a media file
		if !IsMediaFile(imagePath) {
			continue
		}

		wg.Add(1)
		jobs <- imagePath
	}

	// All jobs have been sent
	close(jobs)

	// Close completed and errors channels when all jobs are finished
	go func() {
		wg.Wait()
		close(completed)
		close(errors)
	}()

	// Update progress and handle errors
	for {
		select {
		case ev, ok := <-completed:
			if !ok {
				completed = nil
			} else {
				p.Processed++
				p.Current = ev
				fixerCtx.ProgressCh <- p
			}
		case err, ok := <-errors:
			if !ok {
				errors = nil
			} else {
				Log(LoggerError, "%v", err)
			}
		case <-fixerCtx.Ctx.Done():
			// Let workers finish their current job but dont add new jobs
		}

		if completed == nil && errors == nil {
			break
		}
	}

	return p
}

// ProcessFile processes a single file by finding its sidecar file and then fixing it using the sidecar's metadata
// TODO: This function is written unorganized and should be refactored
func ProcessFile(
	fixerCtx *FixerContext,
	sourcePath string,
	sourceDirName string,
	isYearFolder bool,
) error {
	fileName := filepath.Base(sourcePath)

	// See issue #2
	if fixerCtx.Options.RestoreMOVExtension && strings.EqualFold(filepath.Ext(fileName), ".mp4") {
		majorBrand, err := GetMajorBrand(sourcePath)
		if err == nil && strings.HasPrefix(majorBrand, "Apple QuickTime") {
			ext := filepath.Ext(fileName)
			newName := fileName[:len(fileName)-len(ext)] + ".mov"
			if ext == ".MP4" {
				newName = fileName[:len(fileName)-len(ext)] + ".MOV"
			}
			fileName = newName
		}
	}

	//destPath := filepath.Join(outputPath, fileName)

	sidecarPath, err := FindSidecar(sourcePath)

	if err != nil {
		Log(LoggerError, "Error finding sidecar for file %s: %v", sourcePath, err)
		return err
	}

	// If no sidecar is found and its a video file, try to find a partner image and use it's sidecar
	if sidecarPath == "" && IsVideoFile(sourcePath) {
		partnerImage, err := FindImagePartner(sourcePath)
		if err == nil && partnerImage != "" {
			partnerSidecar, err := FindSidecar(partnerImage)
			if err == nil && partnerSidecar != "" {
				sidecarPath = partnerSidecar
			}
		}
	}

	outputDir, err := ResolveOutputDir(fixerCtx, sourcePath, sidecarPath, sourceDirName, isYearFolder)
	if err != nil {
		return err
	}

	destPath := filepath.Join(outputDir, fileName)

	if _, err := os.Stat(destPath); err == nil {
		Log(LoggerInfo, "File %s already exists, skipping", destPath)
		return nil
	}

	// Metadata sidecar file not found, copy the file without metadata
	if sidecarPath == "" {
		Log(LoggerWarn, "No sidecar file found for %s — copying without metadata", sourcePath)
		if err := CreateFixedFile(fixerCtx, sourcePath, "", destPath, isYearFolder); err != nil {
			Log(LoggerError, "Error creating file without sidecar for %s: %v", sourcePath, err)
			return err
		}
		return nil
	}

	err = CreateFixedFile(fixerCtx, sourcePath, sidecarPath, destPath, isYearFolder)
	if err != nil {
		Log(LoggerError, "Error creating fixed file for %s: %v", sourcePath, err)
		return err
	}

	return nil
}

func CreateFixedFile(
	fixerCtx *FixerContext,
	filePath string,
	fileMetadataPath string,
	destPath string,
	isYearFolder bool,
) error {
	if fixerCtx.Options.DryRun {
		if fixerCtx.Options.UseSymlinks && !isYearFolder {
			Log(LoggerInfo, "[dry-run] Would symlink %s -> %s", destPath, filePath)
		} else {
			Log(LoggerInfo, "[dry-run] Would copy %s -> %s", filePath, destPath)
		}
		if fixerCtx.Options.WriteMetadata && fileMetadataPath != "" {
			Log(LoggerInfo, "[dry-run] Would apply metadata from %s", fileMetadataPath)
		}
		return nil
	}

	// Ensure output directory exists (create if not)
	destDir := filepath.Dir(destPath)
	if _, err := os.Stat(destDir); os.IsNotExist(err) {
		if err := os.MkdirAll(destDir, 0755); err != nil {
			return err
		}

		// Invalidate OutputRoot cache so newly created year folders are visible for symlinks
		ClearCacheDir(fixerCtx.OutputRoot)
	}

	fileName := filepath.Base(destPath)

	if fixerCtx.Options.UseSymlinks && !isYearFolder {
		monthFolder := ""
		if fixerCtx.Options.MonthSubfolders {
			month, err := DetectFileMonth(filePath, fileMetadataPath)
			if err == nil {
				monthFolder = strconv.Itoa(month)
			}
		}

		// Attempt to find the file inside of any year folder in the output
		entries, _ := ReadDirCached(fixerCtx.OutputRoot)
		for _, curEntry := range entries {
			if !curEntry.IsDir() {
				continue
			}

			isYear, _ := IsYearFolder(curEntry.Name())
			if !isYear {
				continue
			}

			targetPaths := []string{}
			if monthFolder != "" {
				targetPaths = append(targetPaths, filepath.Join(fixerCtx.OutputRoot, curEntry.Name(), monthFolder, fileName))
			}
			targetPaths = append(targetPaths, filepath.Join(fixerCtx.OutputRoot, curEntry.Name(), fileName))

			for _, target := range targetPaths {
				if _, err := os.Stat(target); err == nil {
					if err := os.Symlink(target, destPath); err != nil {
						// Symlink failed, continue with normal copy
						if !os.IsExist(err) {
							return fmt.Errorf("Failed to create symlink: %w", err)
						}
					} else {
						// Symlink successful
						return nil
					}
				}
			}
		}
	}

	if err := DuplicateFile(filePath, destPath); err != nil {
		return err
	}

	if fixerCtx.Options.WriteMetadata && fileMetadataPath != "" {
		metadata, err := ReadJsonMetadata(fileMetadataPath)
		if err != nil {
			Log(LoggerError, "Failed to read metadata from %s: %v", fileMetadataPath, err)
		} else {
			// Only apply metadata if reading was successful
			err = ApplyMetadata(destPath, metadata)
			if err != nil {
				Log(LoggerError, "Failed to apply metadata to %s: %v", destPath, err)
			}
		}
	} else if fixerCtx.Options.WriteMetadata && fileMetadataPath == "" {
		Log(LoggerInfo, "WriteMetadata enabled but no sidecar for %s — skipping metadata write", fileName)
	}

	return nil
}
