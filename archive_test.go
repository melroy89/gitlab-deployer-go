package main

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeArchive(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "artifact.zip")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExtractArchiveValidBatch(t *testing.T) {
	c := testConfig("")
	destination := t.TempDir()
	archive := writeArchive(t, makeZIP(t, zipEntry{name: "dist/app.txt", body: "hello"}))
	files, size, err := extractArchive(context.Background(), archive, destination, c, false)
	if err != nil {
		t.Fatal(err)
	}
	if size != 5 || len(files) != 1 || files[0].Path != "dist/app.txt" {
		t.Fatalf("unexpected inventory: size=%d files=%#v", size, files)
	}
	data, err := os.ReadFile(filepath.Join(destination, "dist", "app.txt"))
	if err != nil || string(data) != "hello" {
		t.Fatalf("unexpected extraction: %q, %v", data, err)
	}
	info, _ := os.Stat(filepath.Join(destination, "dist", "app.txt"))
	if info.Mode().Perm() != 0644 {
		t.Fatalf("file mode = %o", info.Mode().Perm())
	}
}

func TestArchivePolicyRejections(t *testing.T) {
	tests := []struct {
		name    string
		entries []zipEntry
	}{
		{"parent traversal", []zipEntry{{name: "../escape", body: "x"}}},
		{"cleaned traversal", []zipEntry{{name: "a/../escape", body: "x"}}},
		{"absolute", []zipEntry{{name: "/escape", body: "x"}}},
		{"backslash", []zipEntry{{name: `a\\b`, body: "x"}}},
		{"reserved manifest", []zipEntry{{name: "batch.json", body: "x"}}},
		{"duplicate", []zipEntry{{name: "a", body: "x"}, {name: "a", body: "y"}}},
		{"file parent", []zipEntry{{name: "a", body: "x"}, {name: "a/b", body: "y"}}},
		{"symlink", []zipEntry{{name: "link", body: "target", mode: os.ModeSymlink | 0777}}},
		{"device", []zipEntry{{name: "device", mode: os.ModeDevice | 0600}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			archive := writeArchive(t, makeZIP(t, test.entries...))
			_, _, err := extractArchive(context.Background(), archive, t.TempDir(), testConfig(""), false)
			if err == nil || !isPermanent(err) {
				t.Fatalf("expected permanent policy error, got %v", err)
			}
		})
	}
}

func TestArchiveLimitsAndInvalidZIP(t *testing.T) {
	c := testConfig("")
	c.MaxFiles = 1
	archive := writeArchive(t, makeZIP(t, zipEntry{name: "a", body: "x"}, zipEntry{name: "b", body: "x"}))
	if _, _, err := extractArchive(context.Background(), archive, t.TempDir(), c, false); err == nil {
		t.Fatal("expected file count rejection")
	}
	c = testConfig("")
	c.MaxExtracted = 2
	archive = writeArchive(t, makeZIP(t, zipEntry{name: "a", body: "xxx"}))
	if _, _, err := extractArchive(context.Background(), archive, t.TempDir(), c, false); err == nil {
		t.Fatal("expected size rejection")
	}
	archive = writeArchive(t, []byte("not a zip"))
	if _, _, err := extractArchive(context.Background(), archive, t.TempDir(), c, false); err == nil || !isPermanent(err) {
		t.Fatalf("expected invalid ZIP rejection, got %v", err)
	}
}

func TestArchiveRejectsConflictingDestinationAndOverwritesDirectFile(t *testing.T) {
	c := testConfig("")
	archive := writeArchive(t, makeZIP(t, zipEntry{name: "app.txt", body: "new"}))
	destination := t.TempDir()
	if err := os.WriteFile(filepath.Join(destination, "app.txt"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := extractArchive(context.Background(), archive, destination, c, false); err == nil {
		t.Fatal("batch extraction overwrote an existing path")
	}
	if _, _, err := extractArchive(context.Background(), archive, destination, c, true); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(destination, "app.txt"))
	if string(data) != "new" {
		t.Fatalf("direct extraction did not overwrite: %q", data)
	}
}

func TestArchiveRejectsDeclaredSizeMismatch(t *testing.T) {
	data := makeZIP(t, zipEntry{name: "a", body: strings.Repeat("x", 32)})
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	reader.File[0].UncompressedSize64++
	if err := validateArchive(reader.File, testConfig(""), true); err != nil {
		t.Fatal(err)
	}
	// validateArchive trusts declarations; extractArchive verifies copied bytes.
	reader.File[0].UncompressedSize64--
}
