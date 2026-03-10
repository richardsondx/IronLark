package search

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type Options struct {
	MaxResults     int
	MaxFileBytes   int
	MaxOutputBytes int
	MaxFiles       int
	ChunkLines     int
}

type Searcher struct {
	HTTPClient   *http.Client
	WebSearchURL string
	UserAgent    string
}

func (s Searcher) SearchFiles(ctx context.Context, root, pattern, glob string, opts Options) ([]string, error) {
	if root == "" {
		root = "."
	}
	if opts.MaxResults <= 0 {
		opts.MaxResults = 20
	}
	if _, err := exec.LookPath("rg"); err == nil {
		args := []string{"--line-number", "--color", "never", "--hidden", "--max-count", fmt.Sprintf("%d", opts.MaxResults)}
		if glob != "" {
			args = append(args, "--glob", glob)
		}
		args = append(args, pattern, root)
		cmd := exec.CommandContext(ctx, "rg", args...)
		out, err := cmd.CombinedOutput()
		lines := trimLines(string(out), opts.MaxResults, opts.MaxOutputBytes)
		if len(lines) > 0 {
			return lines, nil
		}
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
				return nil, nil
			}
			return nil, fmt.Errorf("ripgrep failed: %w", err)
		}
		return nil, nil
	}
	return s.searchFilesFallback(root, pattern, glob, opts)
}

func (s Searcher) SemanticSearch(root, query string, opts Options) ([]string, error) {
	if root == "" {
		root = "."
	}
	if opts.MaxResults <= 0 {
		opts.MaxResults = 8
	}
	if opts.MaxFiles <= 0 {
		opts.MaxFiles = 250
	}
	if opts.ChunkLines <= 0 {
		opts.ChunkLines = 24
	}
	queryTokens := tokenize(query)
	type scoredChunk struct {
		text  string
		score float64
	}
	results := []scoredChunk{}
	filesSeen := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if strings.HasPrefix(name, ".git") || name == "node_modules" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if filesSeen >= opts.MaxFiles {
			return io.EOF
		}
		filesSeen++
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if opts.MaxFileBytes > 0 && len(data) > opts.MaxFileBytes {
			data = data[:opts.MaxFileBytes]
		}
		lines := strings.Split(string(data), "\n")
		for start := 0; start < len(lines); start += opts.ChunkLines {
			end := start + opts.ChunkLines
			if end > len(lines) {
				end = len(lines)
			}
			chunk := strings.Join(lines[start:end], "\n")
			score := scoreChunk(queryTokens, path, chunk)
			if score <= 0 {
				continue
			}
			results = append(results, scoredChunk{
				text:  fmt.Sprintf("%s:%d:%s", path, start+1, summarizeChunk(chunk)),
				score: score,
			})
		}
		return nil
	})
	if err != nil && err != io.EOF {
		return nil, err
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].score == results[j].score {
			return results[i].text < results[j].text
		}
		return results[i].score > results[j].score
	})
	limit := opts.MaxResults
	if len(results) < limit {
		limit = len(results)
	}
	out := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		out = append(out, results[i].text)
	}
	return out, nil
}

func (s Searcher) FetchRules(ctx context.Context, cwd, query string, remoteURLs []string, opts Options) ([]string, error) {
	candidates := []string{
		filepath.Join(cwd, "AGENTS.md"),
		filepath.Join(cwd, ".cursorrules"),
		filepath.Join(cwd, "CLAUDE.md"),
		filepath.Join(cwd, ".lark.yaml"),
	}
	cursorRulesDir := filepath.Join(cwd, ".cursor", "rules")
	_ = filepath.WalkDir(cursorRulesDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.HasSuffix(d.Name(), ".mdc") || strings.HasSuffix(d.Name(), ".md") {
			candidates = append(candidates, path)
		}
		return nil
	})

	queryTokens := tokenize(query)
	results := []string{}
	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		content := string(data)
		if scoreChunk(queryTokens, path, content) <= 0 {
			continue
		}
		results = append(results, fmt.Sprintf("%s: %s", path, summarizeChunk(content)))
		if opts.MaxResults > 0 && len(results) >= opts.MaxResults {
			return results, nil
		}
	}

	for _, rawURL := range remoteURLs {
		if ctx.Err() != nil {
			return results, ctx.Err()
		}
		body, err := s.fetchURL(ctx, rawURL)
		if err != nil {
			continue
		}
		if scoreChunk(queryTokens, rawURL, body) <= 0 {
			continue
		}
		results = append(results, fmt.Sprintf("%s: %s", rawURL, summarizeChunk(body)))
		if opts.MaxResults > 0 && len(results) >= opts.MaxResults {
			break
		}
	}
	return results, nil
}

func (s Searcher) WebSearch(ctx context.Context, query string, opts Options) ([]string, error) {
	endpoint := s.WebSearchURL
	if endpoint == "" {
		endpoint = "https://duckduckgo.com/html/"
	}
	if opts.MaxResults <= 0 {
		opts.MaxResults = 5
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("q", query)
	u.RawQuery = q.Encode()

	body, err := s.fetchURL(ctx, u.String())
	if err != nil {
		return nil, err
	}
	if looksLikeBotChallenge(body) {
		return nil, fmt.Errorf("web search provider blocked the request with an anti-bot challenge")
	}
	re := regexp.MustCompile(`(?s)<a[^>]*class="[^"]*result__a[^"]*"[^>]*href="([^"]+)"[^>]*>(.*?)</a>`)
	snippetRe := regexp.MustCompile(`(?s)<a[^>]*class="[^"]*result__snippet[^"]*"[^>]*>(.*?)</a>|<div[^>]*class="[^"]*result__snippet[^"]*"[^>]*>(.*?)</div>`)
	matches := re.FindAllStringSubmatch(body, opts.MaxResults)
	snippets := snippetRe.FindAllStringSubmatch(body, opts.MaxResults)
	results := []string{}
	for i, match := range matches {
		title := stripHTML(match[2])
		link := decodeURL(match[1])
		snippet := ""
		if i < len(snippets) {
			snippet = stripHTML(firstNonEmpty(snippets[i][1], snippets[i][2]))
		}
		results = append(results, fmt.Sprintf("%s | %s | %s", title, link, snippet))
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("web search returned a page without parseable results")
	}
	return results, nil
}

func (s Searcher) searchFilesFallback(root, pattern, glob string, opts Options) ([]string, error) {
	matches := []string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if strings.HasPrefix(name, ".git") || name == "node_modules" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if glob != "" {
			ok, matchErr := filepath.Match(glob, filepath.Base(path))
			if matchErr != nil || !ok {
				return nil
			}
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if opts.MaxFileBytes > 0 && len(data) > opts.MaxFileBytes {
			data = data[:opts.MaxFileBytes]
		}
		scanner := bufio.NewScanner(bytes.NewReader(data))
		lineNo := 0
		for scanner.Scan() {
			lineNo++
			line := scanner.Text()
			if strings.Contains(line, pattern) {
				matches = append(matches, fmt.Sprintf("%s:%d:%s", path, lineNo, line))
				if len(matches) >= opts.MaxResults {
					return io.EOF
				}
			}
		}
		return nil
	})
	if err != nil && err != io.EOF {
		return nil, err
	}
	return matches, nil
}

func (s Searcher) fetchURL(ctx context.Context, rawURL string) (string, error) {
	client := s.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	if s.UserAgent != "" {
		req.Header.Set("User-Agent", s.UserAgent)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("http %s", resp.Status)
	}
	return string(body), nil
}

func trimLines(text string, maxLines, maxBytes int) []string {
	if maxBytes > 0 && len(text) > maxBytes {
		text = text[:maxBytes]
	}
	lines := []string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lines = append(lines, line)
		if maxLines > 0 && len(lines) >= maxLines {
			break
		}
	}
	return lines
}

func tokenize(value string) []string {
	replacer := strings.NewReplacer("/", " ", "_", " ", "-", " ", ".", " ", "(", " ", ")", " ")
	value = strings.ToLower(replacer.Replace(value))
	parts := strings.Fields(value)
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) < 2 {
			continue
		}
		out = append(out, part)
	}
	return out
}

func looksLikeBotChallenge(body string) bool {
	lower := strings.ToLower(body)
	return strings.Contains(lower, "anomaly-modal") ||
		strings.Contains(lower, "bots use duckduckgo too") ||
		strings.Contains(lower, "complete the following challenge")
}

func scoreChunk(queryTokens []string, path, chunk string) float64 {
	if len(queryTokens) == 0 {
		return 0
	}
	pathTokens := tokenize(path)
	chunkTokens := tokenize(chunk)
	if len(chunkTokens) == 0 && len(pathTokens) == 0 {
		return 0
	}

	bag := map[string]int{}
	for _, token := range pathTokens {
		bag[token] += 2
	}
	for _, token := range chunkTokens {
		bag[token]++
	}

	score := 0.0
	for _, token := range queryTokens {
		if bag[token] > 0 {
			score += float64(bag[token])
			continue
		}
		for candidate, weight := range bag {
			if strings.Contains(candidate, token) || strings.Contains(token, candidate) {
				score += 0.35 * float64(weight)
			}
		}
	}
	return score
}

func summarizeChunk(chunk string) string {
	chunk = stripHTML(chunk)
	chunk = strings.TrimSpace(strings.Join(strings.Fields(chunk), " "))
	if len(chunk) > 220 {
		return chunk[:220] + "..."
	}
	return chunk
}

func stripHTML(value string) string {
	re := regexp.MustCompile(`(?s)<[^>]+>`)
	value = re.ReplaceAllString(value, " ")
	value = strings.ReplaceAll(value, "&amp;", "&")
	value = strings.ReplaceAll(value, "&quot;", "\"")
	value = strings.ReplaceAll(value, "&#39;", "'")
	value = strings.ReplaceAll(value, "&lt;", "<")
	value = strings.ReplaceAll(value, "&gt;", ">")
	return strings.TrimSpace(strings.Join(strings.Fields(value), " "))
}

func decodeURL(value string) string {
	decoded, err := url.QueryUnescape(value)
	if err != nil {
		return value
	}
	return decoded
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
