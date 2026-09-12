package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestDirectModeOverwriteProjectOverrideAndCommandCWD(t *testing.T) {
	archive := makeZIP(t, zipEntry{name: "app.txt", body: "new"})
	destination := t.TempDir()
	if err := os.WriteFile(filepath.Join(destination, "app.txt"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	c := testConfig(destination)
	c.Mode = "direct"
	c.ProjectID = 99
	c.Command = "printf done > command-marker"
	c.CommandCWD = destination
	c.DirectDelay = 0
	d, server := testDownloader(t, c, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/projects/99/jobs/56/artifacts" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write(archive)
	}))
	defer server.Close()
	deployer := &DirectDeployer{config: c, downloader: d}
	deployer.Deploy(context.Background(), testPayload())
	data, _ := os.ReadFile(filepath.Join(destination, "app.txt"))
	if string(data) != "new" {
		t.Fatalf("deployed file = %q", data)
	}
	marker, err := os.ReadFile(filepath.Join(destination, "command-marker"))
	if err != nil || string(marker) != "done" {
		t.Fatalf("command marker=%q err=%v", marker, err)
	}
}
