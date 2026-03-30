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

package cli

import (
	"context"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"github.com/feloex/GoogleTakeoutFixer/internal/fixer"
	version "github.com/feloex/GoogleTakeoutFixer/internal/version"
)

func Main() {
	// Handle logs from the fixer package by printing them
	fixer.LogHandler = func(level fixer.LogLevel, message string) {
		fmt.Printf("[%s] %s\n", level, message)
	}

	// Command-line flags
	showVersion := flag.Bool("version", false, "Show current version")
	inputPath := flag.String("input", "", "Path to Google takeout directory")
	outputPath := flag.String("output", "", "Path to output directory")
	useSymlinks := flag.Bool("symlink", false, "Use symlinks inside of albums instead of duplicating images")
	skipMetadata := flag.Bool("skip-metadata", false, "Skip writing metadata to files")
	ignoreAlbums := flag.Bool("ignore-albums", false, "Ignore all album folders")
	monthSubfolders := flag.Bool("month-subfolders", false, "Create month subfolders (1-12) inside year folders")
	flatten := flag.Bool("flatten", false, "Put all media files directly in the output folder without year/album subfolders")
	restoreMOV := flag.Bool("restore-mov", false, "Restore .MOV file extension in case the Major Brand EXIF field says \"Apple QuickTime (.MOV/QT)\"")
	dryRun := flag.Bool("dry-run", false, "Log what would be done without copying or modifying any files")

	flag.Parse()

	if *showVersion {
		fmt.Println(version.Tag)
		return
	}

	if *flatten && *useSymlinks {
		fmt.Println("Error: --flatten and --symlink cannot be used together")
		os.Exit(1)
	}
	if *flatten && *ignoreAlbums {
		fmt.Println("Error: --flatten and --ignore-albums cannot be used together")
		os.Exit(1)
	}
	if *flatten && *monthSubfolders {
		fmt.Println("Error: --flatten and --month-subfolders cannot be used together")
		os.Exit(1)
	}
	if *useSymlinks && *ignoreAlbums {
		fmt.Println("Error: --symlink and --ignore-albums cannot be used together")
		os.Exit(1)
	}

	if *inputPath == "" || *outputPath == "" {
		fmt.Println("Error: --input and --output are required")
		flag.Usage()
		os.Exit(1)
	}

	progressCh := make(chan fixer.Progress)

	options := fixer.ProcessOptions{
		UseSymlinks:         *useSymlinks,
		WriteMetadata:       !*skipMetadata,
		Flatten:             *flatten,
		IgnoreAlbums:        *ignoreAlbums,
		MonthSubfolders:     *monthSubfolders,
		RestoreMOVExtension: *restoreMOV,
		DryRun:              *dryRun,
	}

	go func() {
		// Invert skipMetadata because the flag is named skipMetadata but the process function expects writeMetadata
		if err := fixer.Process(context.Background(), *inputPath, *outputPath, progressCh, options); err != nil {
			fmt.Printf("Error during processing: %v\n", err)
		}
	}()

	for p := range progressCh {
		if p.Processed == 0 {
			continue
		}

		percentageFinished := math.Round(float64(p.Processed) / float64(p.Total) * 100)

		fmt.Printf("[%3.0f%%] %2d/%2d - %s\n",
			percentageFinished,
			p.Processed,
			p.Total,
			filepath.Base(p.Current),
		)
	}

	fmt.Println("\nDone")
}
