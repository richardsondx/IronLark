package executor

import (
	"context"
	"testing"
	"time"

	"github.com/richardsondx/IronLark/internal/core"
)

type fakeOpsFetcher struct {
	query string
	limit int
}

func (f *fakeOpsFetcher) Fetch(query string, since time.Time, limit int) (string, error) {
	f.query = query
	f.limit = limit
	return `{"watchers":[]}`, nil
}

func TestExecuteFetchOpsUsesFetcher(t *testing.T) {
	exec := testExecutor(t)
	fetcher := &fakeOpsFetcher{}
	exec.OpsFetcher = fetcher

	result, err := exec.Execute(context.Background(), core.Action{
		Type:  core.ActionFetchOps,
		Query: "openclaw",
	}, false)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if fetcher.query != "openclaw" {
		t.Fatalf("expected query to reach fetcher, got %q", fetcher.query)
	}
	if result.Stdout == "" || result.Summary != "fetched operational history" {
		t.Fatalf("unexpected fetch result %#v", result)
	}
}
