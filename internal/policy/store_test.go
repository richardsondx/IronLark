package policy

import (
	"path/filepath"
	"testing"

	"github.com/richardsondx/IronLark/internal/core"
)

func TestNormalizeCommandMatchesEquivalentIPQueries(t *testing.T) {
	first := `printf "IPv4: %s\nIPv6: %s\n" "$(curl -4 -fsS https://api.ipify.org || curl -4 -fsS https://ifconfig.me || echo unknown)" "$(curl -6 -fsS https://api64.ipify.org || curl -6 -fsS https://ifconfig.co || echo unavailable)"`
	second := `sh -lc '
v4=$(curl -fsS4 --max-time 5 https://api.ipify.org || true)
v6=$(curl -fsS6 --max-time 5 https://api64.ipify.org || true)
[ -z "$v4" ] && v4=$(curl -fsS4 --max-time 5 https://ifconfig.me || true)
[ -z "$v6" ] && v6=$(curl -fsS6 --max-time 5 https://ifconfig.co || true)
printf "IPv4: %s\n" "${v4:-unavailable}"
printf "IPv6: %s\n" "${v6:-unavailable}"
'`

	gotFirst := normalizeCommand(first)
	gotSecond := normalizeCommand(second)
	if gotFirst == "" || gotSecond == "" {
		t.Fatalf("expected non-empty normalized commands")
	}
	if gotFirst != gotSecond {
		t.Fatalf("expected equivalent normalization,\nfirst:  %q\nsecond: %q", gotFirst, gotSecond)
	}
}

func TestEvaluateUsesNormalizedAllowRule(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), "policy.json")}
	allowSource := core.Action{
		Type:    core.ActionRunShell,
		Command: `printf "IPv4: %s\n" "$(curl -4 -fsS https://api.ipify.org || curl -4 -fsS https://ifconfig.me || echo unknown)"`,
	}
	if _, err := store.Add(RuleForAction(allowSource, DecisionAllow)); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	match, err := store.Evaluate(core.Action{
		Type: core.ActionRunShell,
		Command: `sh -lc '
v4=$(curl -fsS4 --max-time 5 https://api.ipify.org || true)
[ -z "$v4" ] && v4=$(curl -fsS4 --max-time 5 https://ifconfig.me || true)
printf "IPv4: %s\n" "${v4:-unavailable}"
'`,
	})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if !match.Matched || match.Decision != DecisionAllow {
		t.Fatalf("expected allow match, got %#v", match)
	}
}
