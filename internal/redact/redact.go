package redact

import (
	"regexp"
	"strings"
)

type Redactor struct {
	expressions []*regexp.Regexp
	literals    []string
}

func New(patterns []string) *Redactor {
	r := &Redactor{}
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		r.literals = append(r.literals, pattern)
		r.expressions = append(r.expressions,
			regexp.MustCompile(regexp.QuoteMeta(pattern)+`[=:]\S+`),
			regexp.MustCompile(`(?i)`+regexp.QuoteMeta(pattern)+`\s+[A-Za-z0-9_\-\/+=:.]+`),
		)
	}
	r.expressions = append(r.expressions,
		regexp.MustCompile(`(?i)(api[_-]?key|token|secret|password)\s*[=:]\s*[^\s"']+`),
		regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9_\-\/+=:.]+`),
	)
	return r
}

func (r *Redactor) Text(input string) string {
	out := input
	for _, expr := range r.expressions {
		out = expr.ReplaceAllStringFunc(out, func(match string) string {
			parts := strings.Fields(match)
			if len(parts) == 2 && strings.EqualFold(parts[0], "bearer") {
				return parts[0] + " [REDACTED]"
			}
			if idx := strings.IndexAny(match, "=:"); idx >= 0 {
				return match[:idx+1] + "[REDACTED]"
			}
			return "[REDACTED]"
		})
	}
	for _, literal := range r.literals {
		if strings.Contains(out, literal) {
			out = strings.ReplaceAll(out, literal, literal)
		}
	}
	return out
}
