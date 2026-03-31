package memory

import (
	"fmt"
	"strings"

	"github.com/richardsondx/IronLark/internal/core"
)

// ExtractSessionMemories turns a finished turn into compact facts that can be
// reused by future turns or later compaction passes.
func ExtractSessionMemories(prompt string, response core.LLMResponse, results []core.ActionResult, limit int) []string {
	out := []string{}
	appendUnique := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		for _, existing := range out {
			if existing == value {
				return
			}
		}
		out = append(out, value)
	}

	appendUnique(fmt.Sprintf("User goal: %s", trimSentence(prompt)))
	appendUnique(fmt.Sprintf("Latest summary: %s", trimSentence(response.Summary)))
	for _, finding := range response.Findings {
		appendUnique("Finding: " + trimSentence(finding))
	}
	for _, result := range results {
		if result.Skipped {
			appendUnique(fmt.Sprintf("Skipped %s: %s", result.Action.Type, trimSentence(result.Summary)))
			continue
		}
		target := firstNonEmpty(result.Action.Title, result.Action.Command, result.Action.Path, result.Action.Query)
		if target == "" {
			target = string(result.Action.Type)
		}
		appendUnique(fmt.Sprintf("Executed %s on %s", result.Action.Type, trimSentence(target)))
		if strings.TrimSpace(result.Summary) != "" {
			appendUnique("Result: " + trimSentence(result.Summary))
		}
		if strings.TrimSpace(result.BackgroundRunID) != "" {
			appendUnique("Background run: " + result.BackgroundRunID)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func trimSentence(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 180 {
		return value
	}
	return strings.TrimSpace(value[:177]) + "..."
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
