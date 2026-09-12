package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testDownloader(t *testing.T, config Config, handler http.Handler) (*Downloader, *httptest.Server) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	d := newDownloader(config)
	d.baseURL = server.URL
	d.client.Transport = server.Client().Transport
	return d, server
}

func TestDownloaderUsesExactJobAndToken(t *testing.T) {
	c := testConfig("")
	c.AccessToken = "private"
	body := []byte("artifact")
	d, server := testDownloader(t, c, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/projects/12/jobs/56/artifacts" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("PRIVATE-TOKEN") != "private" {
			t.Errorf("missing private token")
		}
		_, _ = w.Write(body)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "artifact.zip")
	info, err := d.download(context.Background(), 12, 56, path)
	if err != nil {
		t.Fatal(err)
	}
	expected := fmt.Sprintf("%x", sha256.Sum256(body))
	if info.Bytes != int64(len(body)) || info.SHA256 != expected {
		t.Fatalf("download info = %#v", info)
	}
}

func TestDownloaderDirectJobNameURL(t *testing.T) {
	c := testConfig("")
	c.Mode = "direct"
	c.UseJobName = true
	c.Branch = "release/v1"
	c.JobName = "build package"
	d := newDownloader(c)
	url := d.artifactURL(12, 0)
	if !strings.Contains(url, "/artifacts/release%2Fv1/download?job=build+package") {
		t.Fatalf("unexpected URL: %s", url)
	}
}

func TestDownloaderStatusClassification(t *testing.T) {
	for _, status := range []int{408, 429, 500, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			c := testConfig("")
			d, server := testDownloader(t, c, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
			defer server.Close()
			_, err := d.download(context.Background(), 1, 2, filepath.Join(t.TempDir(), "artifact"))
			if err == nil || isPermanent(err) {
				t.Fatalf("expected retryable error, got %v", err)
			}
		})
	}
	for _, status := range []int{400, 401, 403, 404} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			c := testConfig("")
			d, server := testDownloader(t, c, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
			defer server.Close()
			_, err := d.download(context.Background(), 1, 2, filepath.Join(t.TempDir(), "artifact"))
			if err == nil || !isPermanent(err) {
				t.Fatalf("expected permanent error, got %v", err)
			}
		})
	}
}

func TestDownloaderCompressedLimitAndExclusiveCreate(t *testing.T) {
	c := testConfig("")
	c.MaxArtifact = 3
	d, server := testDownloader(t, c, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("four")) }))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "artifact")
	if _, err := d.download(context.Background(), 1, 2, path); err == nil || !isPermanent(err) {
		t.Fatalf("expected limit rejection, got %v", err)
	}
	if err := os.WriteFile(path, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := d.download(context.Background(), 1, 2, path); err == nil {
		t.Fatal("expected exclusive-create failure")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "existing" {
		t.Fatal("existing file was changed")
	}
}

func TestDownloaderRejectsInsecureRedirect(t *testing.T) {
	c := testConfig("")
	d, server := testDownloader(t, c, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "http://example.invalid/artifact")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()
	_, err := d.download(context.Background(), 1, 2, filepath.Join(t.TempDir(), "artifact"))
	if err == nil || !isPermanent(err) {
		t.Fatalf("expected redirect rejection, got %v", err)
	}
}
