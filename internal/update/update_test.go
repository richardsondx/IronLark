package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractBinary(t *testing.T) {
	workdir := t.TempDir()
	archivePath := filepath.Join(workdir, "lark_linux_amd64.tar.gz")
	buf := new(bytes.Buffer)
	gzw := gzip.NewWriter(buf)
	tw := tar.NewWriter(gzw)
	content := []byte("binary")
	if err := tw.WriteHeader(&tar.Header{Name: "lark", Mode: 0o755, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archivePath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	extracted, err := extractBinary(archivePath, workdir, "lark")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(extracted)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "binary" {
		t.Fatalf("unexpected extracted content %q", string(data))
	}
}

func TestReplaceExecutable(t *testing.T) {
	workdir := t.TempDir()
	target := filepath.Join(workdir, "lark")
	source := filepath.Join(workdir, "new-lark")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := replaceExecutable(target, source); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new" {
		t.Fatalf("unexpected target content %q", string(data))
	}
}
