package handler

import (
	"regexp"
	"strings"

	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

var claudeMDRe = regexp.MustCompile(`Contents of (/[^\s]+/CLAUDE\.md)(?: \([^)]*\))?:\s*\n([\s\S]*?)(?:\n\n(?:Contents of /|$))`)

func extractClaudeMDFiles(systemPrompt string) []state.ClaudeMDFile {
	if systemPrompt == "" {
		return nil
	}
	var files []state.ClaudeMDFile
	for _, match := range claudeMDRe.FindAllStringSubmatch(systemPrompt, -1) {
		content := strings.TrimSpace(match[2])
		if content != "" {
			files = append(files, state.ClaudeMDFile{Path: match[1], Content: content})
		}
	}
	if len(files) > 0 {
		return files
	}

	lines := strings.Split(systemPrompt, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, "Contents of /") || !strings.Contains(line, "CLAUDE.md") {
			continue
		}
		parts := strings.SplitN(line, " ", 4)
		if len(parts) < 3 {
			continue
		}
		path := strings.TrimRight(parts[2], ":")
		if idx := strings.Index(path, " ("); idx >= 0 {
			path = path[:idx]
		}
		var contentLines []string
		blankCount := 0
		for j := i + 1; j < len(lines); j++ {
			if strings.HasPrefix(lines[j], "Contents of /") {
				break
			}
			if lines[j] == "" {
				blankCount++
				if blankCount >= 2 {
					break
				}
			} else {
				blankCount = 0
			}
			contentLines = append(contentLines, lines[j])
		}
		content := strings.TrimSpace(strings.Join(contentLines, "\n"))
		if content != "" {
			files = append(files, state.ClaudeMDFile{Path: path, Content: content})
		}
	}
	return files
}
