package policy

import (
	"testing"

	"github.com/richardsondx/IronLark/internal/core"
)

func TestClassifySafeReadCommand(t *testing.T) {
	classifier := NewClassifier([]string{".env"})
	report, err := classifier.Classify(core.Action{
		Type:    core.ActionRun,
		Command: "systemctl status nginx --no-pager",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Level != core.RiskLow {
		t.Fatalf("expected low risk, got %s", report.Level)
	}
}

func TestClassifyMutatingAndHighRiskCommands(t *testing.T) {
	classifier := NewClassifier([]string{".env"})

	mutating, err := classifier.Classify(core.Action{
		Type:    core.ActionRun,
		Command: "sudo apt-get install -y jq",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if mutating.Level != core.RiskMedium || !mutating.NeedsSudo {
		t.Fatalf("expected medium risk with sudo, got %+v", mutating)
	}

	high, err := classifier.Classify(core.Action{
		Type:    core.ActionRun,
		Command: "rm -rf /tmp/bad",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if high.Level != core.RiskHigh {
		t.Fatalf("expected high risk, got %s", high.Level)
	}
}

func TestPatchActionGetsRollbackFlag(t *testing.T) {
	classifier := NewClassifier([]string{"/etc/ssh/sshd_config"})
	report, err := classifier.Classify(core.Action{
		Type: core.ActionPatch,
		Path: "/etc/nginx/nginx.conf",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !report.RollbackAvailable || !report.TouchesSystemFiles {
		t.Fatalf("expected rollback and system file flags, got %+v", report)
	}
}
