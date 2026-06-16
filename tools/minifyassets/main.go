package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tdewolff/minify/v2"
	"github.com/tdewolff/minify/v2/css"
	"github.com/tdewolff/minify/v2/js"
)

func main() {
	paths := os.Args[1:]
	if len(paths) == 0 {
		var err error
		paths, err = discoverAssets()
		if err != nil {
			fatal(err)
		}
	}

	m := minify.New()
	m.AddFunc("text/css", css.Minify)
	m.AddFunc("application/javascript", js.Minify)

	var before, after int64
	for _, path := range paths {
		in, err := os.ReadFile(path)
		if err != nil {
			fatal(fmt.Errorf("%s: %w", path, err))
		}
		mediaType, ok := mediaTypeFor(path)
		if !ok {
			continue
		}
		out, err := m.Bytes(mediaType, in)
		if err != nil {
			fatal(fmt.Errorf("minify %s: %w", path, err))
		}
		before += int64(len(in))
		after += int64(len(out))
		if !bytes.Equal(in, out) {
			if err := os.WriteFile(path, out, 0o644); err != nil {
				fatal(fmt.Errorf("write %s: %w", path, err))
			}
		}
		fmt.Printf("minified %s: %d -> %d bytes\n", filepath.ToSlash(path), len(in), len(out))
	}
	if before > 0 {
		saved := before - after
		fmt.Printf("total: %d -> %d bytes, saved %d bytes (%.1f%%)\n", before, after, saved, float64(saved)*100/float64(before))
	}
}

func discoverAssets() ([]string, error) {
	var paths []string
	for _, dir := range []string{"assets/css", "assets/js"} {
		if _, err := os.Stat(dir); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || strings.Contains(filepath.Base(path), ".min.") {
				return nil
			}
			if _, ok := mediaTypeFor(path); ok {
				paths = append(paths, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func mediaTypeFor(path string) (string, bool) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".css":
		return "text/css", true
	case ".js":
		return "application/javascript", true
	default:
		return "", false
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
