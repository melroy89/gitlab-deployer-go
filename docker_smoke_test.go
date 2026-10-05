//go:build smoke

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json/v2"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestDockerSmoke exercises the packaged service with real HTTP, TLS, storage,
// and process restarts. It is opt-in because it requires Linux and Docker.
func TestDockerSmoke(t *testing.T) {
	image := os.Getenv("ARTIFACT_DEPLOYER_TEST_IMAGE")
	if image == "" {
		image = "artifact-deployer:smoke"
	}
	runID := os.Getenv("ARTIFACT_DEPLOYER_SMOKE_RUN")
	if runID == "" {
		runID = fmt.Sprintf("local-%d", os.Getpid())
	}
	root := t.TempDir()
	archive := makeZIP(t,
		zipEntry{name: "dist/app.txt", body: "smoke artifact"},
		zipEntry{name: "dist/日本語.txt", body: "Unicode delivery"},
	)
	var mu sync.Mutex
	requests := make(map[int]int)
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseDownload := func() { releaseOnce.Do(func() { close(release) }) }
	fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "smoke-artifact-token" {
			t.Error("artifact request did not include the access token")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var job int
		if _, err := fmt.Sscanf(r.URL.Path, "/api/v4/projects/12/jobs/%d/artifacts", &job); err != nil {
			t.Errorf("unexpected artifact path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		mu.Lock()
		requests[job]++
		attempt := requests[job]
		mu.Unlock()
		w.Header().Set("Content-Length", strconv.Itoa(len(archive)))
		if job == 303 && attempt == 1 {
			_, _ = w.Write(archive[:10])
			w.(http.Flusher).Flush()
			close(started)
			select {
			case <-release:
			case <-r.Context().Done():
			}
			_, _ = w.Write(archive[10:])
			return
		}
		_, _ = w.Write(archive)
	}))
	t.Cleanup(fixture.Close)
	t.Cleanup(releaseDownload)
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: fixture.Certificate().Raw})
	if err := os.WriteFile(filepath.Join(root, "ca.pem"), cert, 0644); err != nil {
		t.Fatal(err)
	}
	requestCount := func(job int) int {
		mu.Lock()
		defer mu.Unlock()
		return requests[job]
	}
	docker := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		output, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v: %v\n%s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	docker("image", "inspect", image)
	wait := func(description string, condition func() bool) {
		t.Helper()
		deadline := time.Now().Add(25 * time.Second)
		for time.Now().Before(deadline) {
			if condition() {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s", description)
	}
	client := &http.Client{Timeout: 2 * time.Second}
	request := func(port, path, method, token string, body []byte) (int, map[string]any) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, "http://127.0.0.1:"+port+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Gitlab-Token", token)
		res, err := client.Do(req)
		if err != nil {
			return 0, nil // Startup polling can race with the listener opening.
		}
		defer res.Body.Close()
		var response map[string]any
		if err := json.UnmarshalRead(res.Body, &response); err != nil {
			t.Fatalf("invalid response JSON: %v", err)
		}
		return res.StatusCode, response
	}
	start := func(mode string, extra ...string) (string, string) {
		t.Helper()
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
		_ = listener.Close()
		name := fmt.Sprintf("artifact-smoke-%d-%s", os.Getpid(), port)
		args := []string{"run", "-d", "--name", name, "--label", "org.melroy.artifact-deployer.smoke-run=" + runID, "--network", "host", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
			"--mount", "type=bind,src=" + root + ",dst=/smoke",
			"-e", "GITLAB_SECRET_TOKEN=smoke-secret", "-e", "ACCESS_TOKEN=smoke-artifact-token",
			"-e", "GITLAB_HOSTNAME=" + strings.TrimPrefix(fixture.URL, "https://"),
			"-e", "SSL_CERT_FILE=/smoke/ca.pem", "-e", "DEPLOYMENT_MODE=" + mode,
			"-e", "DESTINATION_PATH=/smoke/" + mode, "-e", "LISTEN_ADDRESS=127.0.0.1:" + port}
		args = append(args, extra...)
		args = append(args, image)
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if t.Failed() {
				logs, _ := exec.CommandContext(ctx, "docker", "logs", name).CombinedOutput()
				t.Logf("%s logs:\n%s", name, logs)
			}
			if output, err := exec.CommandContext(ctx, "docker", "rm", "-f", name).CombinedOutput(); err != nil {
				t.Errorf("remove smoke container: %v\n%s", err, output)
			}
		})
		docker(args...)
		wait(mode+" startup", func() bool {
			status, _ := request(port, "/healthz", http.MethodGet, "", nil)
			return status == http.StatusOK
		})
		wait(mode+" readiness", func() bool {
			status, _ := request(port, "/readyz", http.MethodGet, "", nil)
			return status == http.StatusOK
		})
		return name, port
	}
	webhook := func(port string, job int) (int, map[string]any) {
		t.Helper()
		payload := testPayload()
		payload.DeployableID = int64(job)
		payload.DeploymentID = int64(job + 1000)
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		return request(port, "/gitlab", http.MethodPost, "smoke-secret", body)
	}
	verifyFiles := func(dir string) {
		t.Helper()
		for name, wanted := range map[string]string{"dist/app.txt": "smoke artifact", "dist/日本語.txt": "Unicode delivery"} {
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil || string(data) != wanted {
				t.Fatalf("artifact delivery %s: %q, %v", name, data, err)
			}
		}
	}
	stop := func(name string) {
		t.Helper()
		docker("stop", "--time", "20", name)
		if code := docker("inspect", name, "--format", "{{.State.ExitCode}}"); code != "0" {
			t.Fatalf("unclean shutdown: %s", code)
		}
	}

	direct, port := start("direct", "-e", `POST_DEPLOYMENT_COMMAND=php -r 'file_put_contents("post-command.ok", "ok");'`)
	if status, _ := request(port, "/gitlab", http.MethodPost, "wrong", []byte("{}")); status != http.StatusUnauthorized {
		t.Fatalf("invalid token accepted: %d", status)
	}
	if status, _ := webhook(port, 301); status != http.StatusOK {
		t.Fatalf("direct webhook status: %d", status)
	}
	wait("direct delivery and PHP command", func() bool {
		data, err := os.ReadFile(filepath.Join(root, "direct", "post-command.ok"))
		return err == nil && string(data) == "ok"
	})
	verifyFiles(filepath.Join(root, "direct"))
	stop(direct)
	t.Log("direct startup, authenticated HTTPS download, Unicode extraction, PHP command and shutdown passed")

	batch, port := start("batch")
	batchIDFor := func(job int) string { return fmt.Sprintf("p12-d%d-j%d", job+1000, job) }
	readState := func(job int) BatchRecord {
		data, _ := os.ReadFile(filepath.Join(root, "batch", "state", batchIDFor(job)+".json"))
		var record BatchRecord
		_ = json.Unmarshal(data, &record)
		return record
	}
	verifyReady := func(job int) {
		t.Helper()
		wait("batch ready", func() bool { return readState(job).State == stateReady })
		dir := filepath.Join(root, "batch", "ready", batchIDFor(job))
		verifyFiles(dir)
		data, err := os.ReadFile(filepath.Join(dir, "batch.json"))
		if err != nil {
			t.Fatal(err)
		}
		var manifest BatchManifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			t.Fatal(err)
		}
		if manifest.JobID != int64(job) || manifest.Artifact.Bytes != int64(len(archive)) || manifest.Artifact.SHA256 != fmt.Sprintf("%x", sha256.Sum256(archive)) || len(manifest.Files) != 2 {
			t.Fatalf("invalid manifest: %#v", manifest)
		}
		var total int64
		for _, file := range manifest.Files {
			content, err := os.ReadFile(filepath.Join(dir, file.Path))
			if err != nil || file.Bytes != int64(len(content)) || file.SHA256 != fmt.Sprintf("%x", sha256.Sum256(content)) {
				t.Fatalf("invalid file inventory: %#v, %v", file, err)
			}
			total += file.Bytes
		}
		if manifest.ExtractedSize != total {
			t.Fatalf("extracted size=%d, want %d", manifest.ExtractedSize, total)
		}
	}
	if status, _ := webhook(port, 302); status != http.StatusAccepted {
		t.Fatalf("batch webhook status: %d", status)
	}
	verifyReady(302)
	if status, response := webhook(port, 302); status != http.StatusOK || response["status"] != "duplicate" {
		t.Fatalf("duplicate webhook: %d %v", status, response)
	}
	docker("restart", "--time", "20", batch)
	wait("restart readiness", func() bool {
		status, _ := request(port, "/readyz", http.MethodGet, "", nil)
		return status == http.StatusOK
	})
	if _, response := webhook(port, 302); response["status"] != "duplicate" || requestCount(302) != 1 {
		t.Fatalf("restart lost idempotency: %v requests=%d", response, requestCount(302))
	}
	if status, _ := webhook(port, 303); status != http.StatusAccepted {
		t.Fatalf("recovery webhook status: %d", status)
	}
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("interrupted download did not start")
	}
	docker("kill", "--signal", "KILL", batch)
	releaseDownload()
	docker("start", batch)
	verifyReady(303)
	if record := readState(303); record.Attempts != 2 || requestCount(303) != 2 {
		t.Fatalf("crash recovery: attempts=%d requests=%d", record.Attempts, requestCount(303))
	}
	stop(batch)
	t.Log("batch delivery, manifest, restart idempotency, interrupted-download recovery and shutdown passed")
}
