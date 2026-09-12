package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func startTestProcessor(t *testing.T, handler http.Handler) (*Store, *Processor, context.CancelFunc, *httptest.Server) {
	t.Helper()
	c := testConfig(t.TempDir())
	d, server := testDownloader(t, c, handler)
	store, err := openStore(c.Destination)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	processor := newProcessor(c, store, d)
	ctx, cancel := context.WithCancel(context.Background())
	processor.Start(ctx)
	t.Cleanup(func() { cancel(); processor.Wait(); server.Close() })
	return store, processor, cancel, server
}

func TestProcessorPublishesCompleteBatchOnce(t *testing.T) {
	archive := makeZIP(t, zipEntry{name: "packages/app.deb", body: "package"})
	var requests atomic.Int32
	store, processor, _, _ := startTestProcessor(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write(archive)
	}))
	payload := testPayload()
	record, _, err := store.Accept(payload, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	processor.Notify()
	ready := waitForState(t, store, record.BatchID, stateReady)
	if ready.Attempts != 1 || requests.Load() != 1 {
		t.Fatalf("ready=%#v requests=%d", ready, requests.Load())
	}
	readyPath := store.batchPath(stateReady, record.BatchID)
	data, err := os.ReadFile(filepath.Join(readyPath, "packages", "app.deb"))
	if err != nil || string(data) != "package" {
		t.Fatalf("ready payload: %q err=%v", data, err)
	}
	manifestData, err := os.ReadFile(filepath.Join(readyPath, "batch.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest BatchManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.JobID != payload.DeployableID || manifest.Artifact.Bytes != int64(len(archive)) || len(manifest.Files) != 1 {
		t.Fatalf("manifest = %#v", manifest)
	}
	if _, created, err := store.Accept(payload, time.Now()); err != nil || created {
		t.Fatalf("duplicate ready accept: created=%v err=%v", created, err)
	}
	processor.Notify()
	time.Sleep(10 * time.Millisecond)
	if requests.Load() != 1 {
		t.Fatalf("ready batch downloaded again: %d", requests.Load())
	}
}

func TestProcessorRetriesThenSucceeds(t *testing.T) {
	archive := makeZIP(t, zipEntry{name: "app", body: "ok"})
	var requests atomic.Int32
	store, processor, _, _ := startTestProcessor(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write(archive)
	}))
	record, _, _ := store.Accept(testPayload(), time.Now())
	processor.Notify()
	ready := waitForState(t, store, record.BatchID, stateReady)
	if ready.Attempts != 3 || requests.Load() != 3 {
		t.Fatalf("attempts=%d requests=%d", ready.Attempts, requests.Load())
	}
}

func TestProcessorQuarantinesPermanentFailure(t *testing.T) {
	var requests atomic.Int32
	store, processor, _, _ := startTestProcessor(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	record, _, _ := store.Accept(testPayload(), time.Now())
	processor.Notify()
	failed := waitForState(t, store, record.BatchID, stateQuarantine)
	if failed.Attempts != 1 || requests.Load() != 1 {
		t.Fatalf("attempts=%d requests=%d", failed.Attempts, requests.Load())
	}
}

func TestProcessorQuarantinesAfterEightTransientFailures(t *testing.T) {
	var requests atomic.Int32
	store, processor, _, _ := startTestProcessor(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	record, _, _ := store.Accept(testPayload(), time.Now())
	processor.Notify()
	failed := waitForState(t, store, record.BatchID, stateQuarantine)
	if failed.Attempts != maxAttempts || requests.Load() != maxAttempts {
		t.Fatalf("attempts=%d requests=%d", failed.Attempts, requests.Load())
	}
}
