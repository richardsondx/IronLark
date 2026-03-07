package patches

import (
	"fmt"
	"strconv"
	"strings"
)

type hunk struct {
	origStart int
	lines     []string
}

func ApplyUnifiedDiff(original, diff string) (string, error) {
	origLines, hadTrailingNewline := splitContent(original)
	hunks, err := parseHunks(diff)
	if err != nil {
		return "", err
	}
	if len(hunks) == 0 {
		return "", fmt.Errorf("diff does not contain any hunks")
	}

	out := []string{}
	origIndex := 0
	trailingNewline := hadTrailingNewline

	for _, h := range hunks {
		start := h.origStart - 1
		if start < 0 {
			start = 0
		}
		if start > len(origLines) {
			return "", fmt.Errorf("hunk starts beyond end of file")
		}
		out = append(out, origLines[origIndex:start]...)
		origIndex = start

		for _, line := range h.lines {
			if line == `\ No newline at end of file` {
				trailingNewline = false
				continue
			}
			if line == "" {
				continue
			}
			prefix := line[0]
			text := line[1:]
			switch prefix {
			case ' ':
				if origIndex >= len(origLines) || origLines[origIndex] != text {
					return "", fmt.Errorf("context mismatch while applying diff")
				}
				out = append(out, origLines[origIndex])
				origIndex++
			case '-':
				if origIndex >= len(origLines) || origLines[origIndex] != text {
					return "", fmt.Errorf("delete mismatch while applying diff")
				}
				origIndex++
			case '+':
				out = append(out, text)
			default:
				return "", fmt.Errorf("unsupported diff line %q", line)
			}
		}
	}

	out = append(out, origLines[origIndex:]...)
	joined := strings.Join(out, "\n")
	if trailingNewline && joined != "" {
		joined += "\n"
	}
	return joined, nil
}

func splitContent(content string) ([]string, bool) {
	if content == "" {
		return nil, false
	}
	hadTrailingNewline := strings.HasSuffix(content, "\n")
	if hadTrailingNewline {
		content = strings.TrimSuffix(content, "\n")
	}
	return strings.Split(content, "\n"), hadTrailingNewline
}

func parseHunks(diff string) ([]hunk, error) {
	lines := strings.Split(strings.ReplaceAll(diff, "\r\n", "\n"), "\n")
	hunks := []hunk{}
	var current *hunk

	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "@@"):
			start, err := parseOrigStart(line)
			if err != nil {
				return nil, err
			}
			h := hunk{origStart: start}
			hunks = append(hunks, h)
			current = &hunks[len(hunks)-1]
		case strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ "):
			continue
		case current != nil:
			current.lines = append(current.lines, line)
		}
	}
	return hunks, nil
}

func parseOrigStart(header string) (int, error) {
	parts := strings.Split(header, " ")
	if len(parts) < 3 {
		return 0, fmt.Errorf("invalid hunk header %q", header)
	}
	rangePart := strings.TrimPrefix(parts[1], "-")
	rangePart = strings.TrimSuffix(rangePart, "@@")
	start := strings.Split(rangePart, ",")[0]
	value, err := strconv.Atoi(start)
	if err != nil {
		return 0, fmt.Errorf("parse hunk start: %w", err)
	}
	return value, nil
}
