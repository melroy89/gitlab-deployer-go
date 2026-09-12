package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreAcceptanceDuplicateAndRecovery(t *testing.T) {
	root := t.TempDir()
	store, err := openStore(root)
	if err != nil {
		t.Fatal(err)
	}
	payload := testPayload()
	now := time.Unix(100, 0)
	record, created, err := store.Accept(payload, now)
	if err != nil || !created {
		t.Fatalf("accept: created=%v err=%v", created, err)
	}
	if record.SHA != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("full deployment SHA = %q", record.SHA)
	}
	duplicate, created, err := store.Accept(payload, now.Add(time.Second))
	if err != nil || created || duplicate.BatchID != record.BatchID {
		t.Fatalf("duplicate: %#v created=%v err=%v", duplicate, created, err)
	}
	claimed, err := store.Claim(record.BatchID, now)
	if err != nil || claimed == nil || claimed.Attempts != 1 {
		t.Fatalf("claim: %#v err=%v", claimed, err)
	}
	restarted, err := openStore(root)
	if err != nil {
		t.Fatal(err)
	}
	recovered, _ := restarted.Record(record.BatchID)
	if recovered.State != statePending || recovered.Attempts != 1 {
		t.Fatalf("recovered = %#v", recovered)
	}
	if _, err := os.Stat(restarted.batchPath(statePending, record.BatchID)); err != nil {
		t.Fatal(err)
	}
}

func TestStoreRejectsConflictingDuplicateIdentity(t *testing.T) {
	store, err := openStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	payload := testPayload()
	if _, _, err := store.Accept(payload, time.Now()); err != nil {
		t.Fatal(err)
	}
	payload.CommitURL = "https://gitlab.example/project/-/commit/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, _, err := store.Accept(payload, time.Now()); !errors.Is(err, errBatchConflict) {
		t.Fatalf("expected batch conflict, got %v", err)
	}
}

func TestStoreRetryAndQuarantine(t *testing.T) {
	store, err := openStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	record, _, _ := store.Accept(testPayload(), time.Now())
	_, _ = store.Claim(record.BatchID, time.Now())
	next := time.Now().Add(time.Minute)
	failed, err := store.Fail(record.BatchID, errors.New("network failed"), &next, time.Now())
	if err != nil || failed.State != statePending || failed.NextAttemptAt == nil {
		t.Fatalf("retry state: %#v err=%v", failed, err)
	}
	if due := store.Due(time.Now()); len(due) != 0 {
		t.Fatalf("future retry was due: %v", due)
	}
	_, _ = store.Claim(record.BatchID, next)
	failed, err = store.Fail(record.BatchID, permanent("invalid archive"), nil, next)
	if err != nil || failed.State != stateQuarantine {
		t.Fatalf("quarantine state: %#v err=%v", failed, err)
	}
}

func TestStoreRecoversReadyRenameBeforeJournalUpdate(t *testing.T) {
	root := t.TempDir()
	store, _ := openStore(root)
	record, _, _ := store.Accept(testPayload(), time.Now())
	_, _ = store.Claim(record.BatchID, time.Now())
	processing := store.batchPath(stateProcessing, record.BatchID)
	extracted := filepath.Join(processing, "extracted")
	if err := os.Mkdir(extracted, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(extracted, store.batchPath(stateReady, record.BatchID)); err != nil {
		t.Fatal(err)
	}
	restarted, err := openStore(root)
	if err != nil {
		t.Fatal(err)
	}
	recovered, _ := restarted.Record(record.BatchID)
	if recovered.State != stateReady {
		t.Fatalf("state = %s", recovered.State)
	}
}

func TestStoreAllowsConsumerToClaimReadyDirectory(t *testing.T) {
	root := t.TempDir()
	store, _ := openStore(root)
	record, _, _ := store.Accept(testPayload(), time.Now())
	claimed, _ := store.Claim(record.BatchID, time.Now())
	extracted := filepath.Join(store.batchPath(stateProcessing, record.BatchID), "extracted")
	if err := os.Mkdir(extracted, 0755); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(claimed.BatchID, extracted, DownloadInfo{SHA256: "abc", Bytes: 1}, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	consumerPath := filepath.Join(root, "consumer-claimed")
	if err := os.Rename(store.batchPath(stateReady, record.BatchID), consumerPath); err != nil {
		t.Fatal(err)
	}
	restarted, err := openStore(root)
	if err != nil {
		t.Fatal(err)
	}
	recovered, ok := restarted.Record(record.BatchID)
	if !ok || recovered.State != stateReady {
		t.Fatalf("consumed ready record = %#v, exists=%v", recovered, ok)
	}
}
