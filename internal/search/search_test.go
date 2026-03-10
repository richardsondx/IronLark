package search

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSearchFilesFallbackFindsMatches(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "main.go")
	if err := os.WriteFile(target, []byte("package main\nfunc runSearch() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	searcher := Searcher{}
	results, err := searcher.searchFilesFallback(root, "runSearch", "*.go", Options{MaxResults: 5, MaxFileBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !strings.Contains(results[0], "main.go:2") {
		t.Fatalf("unexpected results: %#v", results)
	}
}

func TestSemanticSearchRanksRelevantChunk(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "agent.txt"), []byte("The terminal agent can inspect repositories and execute shell commands safely.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "other.txt"), []byte("Bananas are yellow.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	searcher := Searcher{}
	results, err := searcher.SemanticSearch(root, "safe terminal shell agent", Options{MaxResults: 2, MaxFiles: 10, ChunkLines: 8})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 || !strings.Contains(results[0], "agent.txt") {
		t.Fatalf("unexpected semantic results: %#v", results)
	}
}

func TestFetchRulesIncludesLocalRules(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Use ripgrep for searches.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	searcher := Searcher{}
	results, err := searcher.FetchRules(context.Background(), root, "ripgrep", nil, Options{MaxResults: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 || !strings.Contains(results[0], "AGENTS.md") {
		t.Fatalf("unexpected rules results: %#v", results)
	}
}

func TestWebSearchParsesResultMarkup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><body><a class="result__a" href="https://example.com/doc">Example Doc</a><div class="result__snippet">Search snippet</div></body></html>`))
	}))
	defer server.Close()

	searcher := Searcher{HTTPClient: server.Client(), WebSearchURL: server.URL}
	results, err := searcher.WebSearch(context.Background(), "example", Options{MaxResults: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !strings.Contains(results[0], "https://example.com/doc") {
		t.Fatalf("unexpected web results: %#v", results)
	}
}

func TestWebSearchReturnsExplicitBotChallengeError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><body><div class="anomaly-modal__title">Unfortunately, bots use DuckDuckGo too.</div></body></html>`))
	}))
	defer server.Close()

	searcher := Searcher{HTTPClient: server.Client(), WebSearchURL: server.URL}
	_, err := searcher.WebSearch(context.Background(), "example", Options{MaxResults: 3})
	if err == nil || !strings.Contains(err.Error(), "anti-bot challenge") {
		t.Fatalf("expected anti-bot challenge error, got %v", err)
	}
}
