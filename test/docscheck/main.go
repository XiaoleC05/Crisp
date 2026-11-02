// Command docscheck verifies that relative Markdown links in the repository
// resolve to existing files. It is used by CI and by contributors before
// committing documentation changes:
//
//	go run ./test/docscheck
//
// Links inside fenced code blocks, absolute URLs, mailto: links and pure
// anchors are ignored. A missing target makes the command exit non-zero.
package main

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// linkPattern matches Markdown links of the form [label](target "optional title").
var linkPattern = regexp.MustCompile("\\]\\(([^)\\s]+)(?:\\s+\"[^\"]*\")?\\)")

func main() {
	root := flag.String("root", ".", "repository root to scan")
	flag.Parse()

	var checked, broken int
	walkErr := filepath.WalkDir(*root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := entry.Name()
		if entry.IsDir() {
			switch name {
			case ".git", ".tmp", "bin", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(name), ".md") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		lineNo := 0
		inFence := false
		for _, line := range strings.Split(string(data), "\n") {
			lineNo++
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "\x60\x60\x60") || strings.HasPrefix(trimmed, "~~~") {
				inFence = !inFence
				continue
			}
			if inFence {
				continue
			}
			for _, match := range linkPattern.FindAllStringSubmatch(line, -1) {
				target := match[1]
				if target == "" ||
					strings.HasPrefix(target, "http://") ||
					strings.HasPrefix(target, "https://") ||
					strings.HasPrefix(target, "mailto:") ||
					strings.HasPrefix(target, "#") {
					continue
				}
				if idx := strings.IndexByte(target, '#'); idx >= 0 {
					target = target[:idx]
				}
				if target == "" {
					continue
				}
				checked++
				resolved := filepath.Join(filepath.Dir(path), filepath.FromSlash(target))
				if _, statErr := os.Stat(resolved); statErr != nil {
					broken++
					fmt.Fprintf(os.Stderr, "%s:%d: missing link target %q\n", path, lineNo, match[1])
				}
			}
		}
		return nil
	})
	if walkErr != nil {
		fmt.Fprintln(os.Stderr, "docscheck:", walkErr)
		os.Exit(2)
	}
	fmt.Printf("docscheck: %d relative links checked, %d broken\n", checked, broken)
	if broken > 0 {
		os.Exit(1)
	}
}
