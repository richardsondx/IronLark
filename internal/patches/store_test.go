package patches

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplyUnifiedDiffAndUndo(t *testing.T) {
	workdir := t.TempDir()
	target := filepath.Join(workdir, "example.conf")
	store := Store{Dir: filepath.Join(workdir, "patches")}
	original := "listen 3000;\n"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	diff := `--- example.conf
+++ example.conf
@@ -1,1 +1,1 @@
-listen 3000;
+listen 4000;`

	record, err := store.Apply(target, diff)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(updated) != "listen 4000;\n" {
		t.Fatalf("unexpected patched content %q", string(updated))
	}

	if _, err := store.Undo(record.ID); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != original {
		t.Fatalf("expected original content restored, got %q", string(restored))
	}
}
