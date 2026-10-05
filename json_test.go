package main

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestWebhookStrictJSON(t *testing.T) {
	app, err := newApp(t.Context(), testConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, body string
		status     int
		reason     string
	}{
		{"duplicate", `{"object_kind":"job","object_kind":"deployment"}`, 400, "invalid_json"},
		{"escaped duplicate", `{"object_kind":"job","object_\u006bind":"deployment"}`, 400, "invalid_json"},
		{"nested duplicate", `{"project":{"id":1,"id":2}}`, 400, "invalid_json"},
		{"unknown nested duplicate", `{"extra":{"id":1,"id":2}}`, 400, "invalid_json"},
		{"invalid UTF-8", "{\"object_kind\":\"\xff\"}", 400, "invalid_json"},
		{"unpaired surrogate", `{"object_kind":"\ud800"}`, 400, "invalid_json"},
		{"truncated", `{"object_kind":`, 400, "invalid_json"},
		{"empty", ``, 400, "invalid_json"},
		{"second document", `{} {}`, 400, "invalid_json"},
		{"trailing garbage", `{} x`, 400, "invalid_json"},
		{"whitespace", "{} \n\t", 200, "not_deployment"},
		{"wrong kind casing", `{"Object_kind":"deployment"}`, 200, "not_deployment"},
		{"wrong status casing", `{"object_kind":"deployment","Status":"success"}`, 200, "deployment_not_successful"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := webhookRequest(t, app.routes(), http.MethodPost, "secret", []byte(test.body))
			if response.Code != test.status || !strings.Contains(response.Body.String(), test.reason) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
	if len(app.store.records) != 0 {
		t.Fatal("invalid or ignored input persisted a deployment")
	}
}

func TestWebhookBodyLimit(t *testing.T) {
	app, err := newApp(t.Context(), testConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{maxWebhookBody - 1, maxWebhookBody, maxWebhookBody + 1} {
		// The excess byte is trailing whitespace, so the decoder must check EOF
		// rather than stop after the first complete value.
		body := []byte("{}" + strings.Repeat(" ", size-2))
		response := webhookRequest(t, app.routes(), http.MethodPost, "secret", body)
		want := http.StatusOK
		if size > maxWebhookBody {
			want = http.StatusBadRequest
		}
		if response.Code != want {
			t.Fatalf("size=%d status=%d body=%s", size, response.Code, response.Body.String())
		}
	}
	response := webhookRequest(t, app.routes(), http.MethodPost, "wrong", []byte("{"))
	if response.Code != http.StatusUnauthorized {
		t.Fatal("JSON decoding preceded authentication")
	}
}

func TestWebhookUnknownFieldsAndLargeIDs(t *testing.T) {
	app, err := newApp(t.Context(), testConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	payload := testPayload()
	payload.Project.ID = 9007199254740993
	payload.DeploymentID = 9007199254740995
	payload.DeployableID = 9007199254740997
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data[:len(data)-1], []byte(`,"extra":{"metadata":true}}`)...)
	response := webhookRequest(t, app.routes(), http.MethodPost, "secret", data)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	record, ok := app.store.Record(batchID(payload))
	if !ok || record.ProjectID != payload.Project.ID || record.DeploymentID != payload.DeploymentID || record.JobID != payload.DeployableID {
		t.Fatalf("identity changed: %#v", record)
	}
}

func TestJSONResponseEncodingFailure(t *testing.T) {
	for _, bad := range []any{map[string]string{"value": "\xff"}, make(chan int)} {
		response := httptest.NewRecorder()
		writeJSON(response, http.StatusAccepted, bad)
		if response.Code != http.StatusInternalServerError || response.Body.String() != "{\"status\":\"error\",\"reason\":\"encoding_failed\"}\n" {
			t.Fatalf("encoding failure leaked output: status=%d body=%s", response.Code, response.Body.String())
		}
	}
}

func TestStoreEncodingFailurePreservesJournal(t *testing.T) {
	store, err := openStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	record, _, err := store.Accept(testPayload(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(store.statePath(record.BatchID))
	if err != nil {
		t.Fatal(err)
	}
	record.ProjectName = "\xff"
	if err := store.writeRecord(record); err == nil {
		t.Fatal("invalid UTF-8 was persisted")
	}
	current, err := os.ReadFile(store.statePath(record.BatchID))
	if err != nil || !bytes.Equal(original, current) {
		t.Fatalf("journal changed after failure: err=%v", err)
	}
	entries, err := os.ReadDir(filepath.Join(store.root, "state"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary journal leaked: entries=%v err=%v", entries, err)
	}
	info, err := os.Stat(store.statePath(record.BatchID))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("journal permissions changed: info=%v err=%v", info, err)
	}
}

func TestStoreRejectsMalformedJournal(t *testing.T) {
	for _, data := range []string{`{"schema_version":1,"schema_version":1}`, "{\"batch_id\":\"\xff\"}", `{`} {
		root := t.TempDir()
		store, err := openStore(root)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(store.statePath("bad"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := openStore(root); err == nil {
			t.Fatal("malformed journal did not stop startup")
		}
	}
}

type jsonTestWriter struct {
	writes int
	bytes  int
	err    error
}

func (w *jsonTestWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.err != nil {
		return 0, w.err
	}
	w.bytes += len(p)
	return len(p), nil
}

func TestManifestStreamingAndWriterFailure(t *testing.T) {
	manifest := BatchManifest{SchemaVersion: 1, Files: make([]InventoryFile, 3000)}
	for i := range manifest.Files {
		manifest.Files[i] = InventoryFile{Path: "dist/package.deb", Bytes: 4096, SHA256: strings.Repeat("a", 64)}
	}
	writer := &jsonTestWriter{}
	if err := marshalJSONFile(writer, &manifest); err != nil {
		t.Fatal(err)
	}
	if writer.writes < 2 || writer.bytes < 300000 {
		t.Fatalf("large manifest was not streamed: %#v", writer)
	}
	writer = &jsonTestWriter{err: io.ErrClosedPipe}
	if err := marshalJSONFile(writer, &manifest); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("writer failure not propagated: %v", err)
	}
	path := filepath.Join(t.TempDir(), "batch.json")
	if err := writeJSONFile(path, &manifest, 0644); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded BatchManifest
	if err := json.Unmarshal(data, &decoded); err != nil || !reflect.DeepEqual(decoded, manifest) {
		t.Fatalf("cannot read streamed manifest: err=%v", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0644 {
		t.Fatalf("manifest permissions changed: info=%v err=%v", info, err)
	}
}
