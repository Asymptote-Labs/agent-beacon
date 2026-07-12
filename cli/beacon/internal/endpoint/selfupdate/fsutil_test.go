package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestSafeJoinRejectsTraversal(t *testing.T) {
	dest := "/tmp/dest"
	bad := []string{
		"..",
		"../escape",
		"../../etc/passwd",
		"a/../../escape",
		"a/../b",
		"a/..",
		"a\\..\\..\\escape",
		"..\\escape",
		"/etc/passwd",
		"\\windows\\system32",
	}
	for _, name := range bad {
		if _, err := safeJoin(dest, name); err == nil {
			t.Errorf("safeJoin(%q) should be rejected", name)
		}
	}

	ok := map[string]string{
		"opt/beacon/bin/beacon": "/tmp/dest/opt/beacon/bin/beacon",
		"a/b/c":                 "/tmp/dest/a/b/c",
		"file":                  "/tmp/dest/file",
	}
	for name, want := range ok {
		got, err := safeJoin(dest, name)
		if err != nil {
			t.Errorf("safeJoin(%q) unexpected error: %v", name, err)
			continue
		}
		if got != want {
			t.Errorf("safeJoin(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestExtractTarballRejectsTraversal(t *testing.T) {
	// Build a tarball with a traversal entry and confirm extraction refuses it
	// and writes nothing outside dest.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("pwned")
	hdr := &tar.Header{Name: "../escape.txt", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "dest")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	tarball := filepath.Join(dir, "a.tar.gz")
	if err := os.WriteFile(tarball, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := extractTarballInto(tarball, dest); err == nil {
		t.Fatal("expected extraction to reject traversal entry")
	}
	if _, err := os.Stat(filepath.Join(dir, "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("traversal file was written outside dest: %v", err)
	}
}
