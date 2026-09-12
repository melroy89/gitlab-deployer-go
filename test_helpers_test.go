package main

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"testing"
	"time"
)

type zipEntry struct {
	name string
	body string
	mode os.FileMode
}

func makeZIP(t *testing.T, entries ...zipEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		if entry.mode != 0 {
			header.SetMode(entry.mode)
		}
		file, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(file, entry.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func testConfig(root string) Config {
	return Config{
		Mode: "batch", Secret: "secret", Host: "gitlab.example", Destination: root,
		Workers: 1, MaxFiles: 100, MaxArtifact: 1 << 20, MaxExtracted: 1 << 20,
		RetryBase: time.Millisecond, RetryMax: 4 * time.Millisecond, ScanInterval: time.Millisecond,
	}
}

func testPayload() GitLabPayload {
	var payload GitLabPayload
	payload.ObjectKind = "deployment"
	payload.Status = "success"
	payload.Project.ID = 12
	payload.Project.Name = "project"
	payload.Project.WebURL = "https://gitlab.example/project"
	payload.DeploymentID = 34
	payload.DeployableID = 56
	payload.Environment = "production"
	payload.ShortSHA = "01234567"
	payload.CommitURL = "https://gitlab.example/project/-/commit/0123456789abcdef0123456789abcdef01234567"
	payload.User.Username = "deployer"
	return payload
}

func waitForState(t *testing.T, store *Store, id string, wanted BatchState) *BatchRecord {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if record, ok := store.Record(id); ok && record.State == wanted {
			return record
		}
		time.Sleep(time.Millisecond)
	}
	record, _ := store.Record(id)
	t.Fatalf("batch did not reach %s; final record: %#v", wanted, record)
	return nil
}
