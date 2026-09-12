package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type Store struct {
	root    string
	mu      sync.Mutex
	records map[string]*BatchRecord
}

var errBatchConflict = errors.New("batch identity already exists with conflicting metadata")

type completionStateError struct{ err error }

func (e *completionStateError) Error() string { return e.err.Error() }
func (e *completionStateError) Unwrap() error { return e.err }

var fullCommitSHA = regexp.MustCompile(`(?i)^[0-9a-f]{40}([0-9a-f]{24})?$`)

func deploymentSHA(payload GitLabPayload) string {
	if fullCommitSHA.MatchString(payload.SHA) {
		return strings.ToLower(payload.SHA)
	}
	if commitURL, err := url.Parse(payload.CommitURL); err == nil {
		candidate := pathpkg.Base(commitURL.Path)
		if fullCommitSHA.MatchString(candidate) {
			return strings.ToLower(candidate)
		}
	}
	return strings.ToLower(payload.ShortSHA)
}

func openStore(root string) (*Store, error) {
	s := &Store{root: root, records: make(map[string]*BatchRecord)}
	for _, name := range []string{string(statePending), string(stateProcessing), string(stateReady), string(stateQuarantine), "state"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0755); err != nil {
			return nil, fmt.Errorf("create spool directory %s: %w", name, err)
		}
	}
	if err := syncDirectory(root); err != nil {
		return nil, err
	}
	if err := s.loadAndRecover(); err != nil {
		return nil, err
	}
	return s, nil
}

func batchID(payload GitLabPayload) string {
	return fmt.Sprintf("p%d-d%d-j%d", payload.Project.ID, payload.DeploymentID, payload.DeployableID)
}

func (s *Store) statePath(id string) string { return filepath.Join(s.root, "state", id+".json") }
func (s *Store) batchPath(state BatchState, id string) string {
	return filepath.Join(s.root, string(state), id)
}

func cloneRecord(record *BatchRecord) *BatchRecord {
	copy := *record
	if record.NextAttemptAt != nil {
		next := *record.NextAttemptAt
		copy.NextAttemptAt = &next
	}
	if record.Artifact != nil {
		artifact := *record.Artifact
		copy.Artifact = &artifact
	}
	return &copy
}

func (s *Store) loadAndRecover() error {
	entries, err := os.ReadDir(filepath.Join(s.root, "state"))
	if err != nil {
		return fmt.Errorf("read state directory: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.root, "state", entry.Name()))
		if err != nil {
			return fmt.Errorf("read batch state %s: %w", entry.Name(), err)
		}
		var record BatchRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return fmt.Errorf("decode batch state %s: %w", entry.Name(), err)
		}
		if record.SchemaVersion != stateSchemaVersion || record.BatchID+".json" != entry.Name() {
			return fmt.Errorf("invalid batch state identity in %s", entry.Name())
		}
		s.records[record.BatchID] = &record
	}
	if err := s.removeEmptyPendingOrphans(); err != nil {
		return err
	}
	for id, record := range s.records {
		if err := s.recoverRecord(id, record); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) removeEmptyPendingOrphans() error {
	dir := filepath.Join(s.root, string(statePending))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if _, known := s.records[entry.Name()]; known {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		children, err := os.ReadDir(path)
		if err != nil || !entry.IsDir() || len(children) != 0 {
			return fmt.Errorf("unknown non-empty pending spool entry %s", entry.Name())
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return syncDirectory(dir)
}

func (s *Store) recoverRecord(id string, record *BatchRecord) error {
	readyPath := s.batchPath(stateReady, id)
	if record.State == stateProcessing {
		if info, err := os.Stat(readyPath); err == nil && info.IsDir() {
			record.State = stateReady
			record.NextAttemptAt = nil
			record.LastError = ""
			record.UpdatedAt = time.Now().UTC()
			if err := s.writeRecord(record); err != nil {
				return err
			}
			_ = os.RemoveAll(s.batchPath(stateProcessing, id))
			return nil
		}
		if info, err := os.Stat(s.batchPath(stateQuarantine, id)); err == nil && info.IsDir() {
			record.State = stateQuarantine
			record.NextAttemptAt = nil
			record.UpdatedAt = time.Now().UTC()
			return s.writeRecord(record)
		}
		from := s.batchPath(stateProcessing, id)
		to := s.batchPath(statePending, id)
		if _, err := os.Stat(from); err == nil {
			if _, pendingErr := os.Stat(to); pendingErr == nil {
				return fmt.Errorf("batch %s exists in pending and processing", id)
			} else if !errors.Is(pendingErr, os.ErrNotExist) {
				return pendingErr
			}
			_ = os.RemoveAll(filepath.Join(from, "attempt"))
			if err := os.Rename(from, to); err != nil {
				return fmt.Errorf("recover processing batch %s: %w", id, err)
			}
		} else if errors.Is(err, os.ErrNotExist) {
			pendingInfo, pendingErr := os.Stat(to)
			if errors.Is(pendingErr, os.ErrNotExist) {
				pendingErr = os.Mkdir(to, 0755)
			}
			if pendingErr != nil {
				return pendingErr
			}
			if pendingInfo != nil && !pendingInfo.IsDir() {
				return fmt.Errorf("pending batch %s is not a directory", id)
			}
		} else {
			return err
		}
		record.State = statePending
		record.UpdatedAt = time.Now().UTC()
		return s.writeRecord(record)
	}
	expected := s.batchPath(record.State, id)
	if record.State != statePending && record.State != stateReady && record.State != stateQuarantine {
		return fmt.Errorf("batch %s has unknown state %q", id, record.State)
	}
	if record.State == statePending {
		processing := s.batchPath(stateProcessing, id)
		if info, err := os.Stat(processing); err == nil && info.IsDir() {
			if _, pendingErr := os.Stat(expected); pendingErr == nil {
				return fmt.Errorf("batch %s exists in pending and processing", id)
			} else if !errors.Is(pendingErr, os.ErrNotExist) {
				return pendingErr
			}
			_ = os.RemoveAll(filepath.Join(processing, "attempt"))
			if err := os.Rename(processing, expected); err != nil {
				return err
			}
		}
	}
	if info, err := os.Stat(expected); err != nil || !info.IsDir() {
		if record.State == stateReady {
			if errors.Is(err, os.ErrNotExist) {
				// A downstream consumer owns the directory after atomically moving it
				// out of ready/. The durable state remains terminal and idempotent.
				return nil
			}
			return fmt.Errorf("ready batch %s has an invalid handoff path", id)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Mkdir(expected, 0755); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	if record.State == stateReady {
		_ = os.RemoveAll(s.batchPath(stateProcessing, id))
	}
	return nil
}

func (s *Store) Accept(payload GitLabPayload, now time.Time) (*BatchRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := batchID(payload)
	if existing, ok := s.records[id]; ok {
		sha := deploymentSHA(payload)
		if existing.Environment != payload.Environment || (existing.SHA != "" && sha != "" && existing.SHA != sha) {
			return nil, false, errBatchConflict
		}
		return cloneRecord(existing), false, nil
	}
	sha := deploymentSHA(payload)
	record := &BatchRecord{
		SchemaVersion: stateSchemaVersion, BatchID: id, State: statePending,
		ProjectID: payload.Project.ID, DeploymentID: payload.DeploymentID, JobID: payload.DeployableID,
		Environment: payload.Environment, SHA: sha, CommitURL: payload.CommitURL,
		ProjectName: payload.Project.Name, ProjectURL: payload.Project.WebURL, TriggeredBy: payload.User.Username,
		AcceptedAt: now.UTC(), UpdatedAt: now.UTC(),
	}
	if err := os.Mkdir(s.batchPath(statePending, id), 0755); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, false, fmt.Errorf("batch directory %s exists without state", id)
		}
		return nil, false, err
	}
	if err := syncDirectory(filepath.Join(s.root, string(statePending))); err != nil {
		return nil, false, err
	}
	if err := s.writeRecord(record); err != nil {
		_ = os.Remove(s.batchPath(statePending, id))
		return nil, false, err
	}
	s.records[id] = record
	return cloneRecord(record), true, nil
}

func (s *Store) Due(now time.Time) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for id, record := range s.records {
		if record.State == statePending && (record.NextAttemptAt == nil || !record.NextAttemptAt.After(now)) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func (s *Store) Claim(id string, now time.Time) (*BatchRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[id]
	if !ok || record.State != statePending || (record.NextAttemptAt != nil && record.NextAttemptAt.After(now)) {
		return nil, nil
	}
	if err := os.Rename(s.batchPath(statePending, id), s.batchPath(stateProcessing, id)); err != nil {
		return nil, fmt.Errorf("claim batch %s: %w", id, err)
	}
	record.State = stateProcessing
	record.Attempts++
	record.UpdatedAt = now.UTC()
	record.NextAttemptAt = nil
	record.LastError = ""
	if err := s.writeRecord(record); err != nil {
		_ = os.Rename(s.batchPath(stateProcessing, id), s.batchPath(statePending, id))
		record.State = statePending
		record.Attempts--
		return nil, err
	}
	return cloneRecord(record), nil
}

func (s *Store) Fail(id string, cause error, retryAt *time.Time, now time.Time) (*BatchRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[id]
	if !ok || record.State != stateProcessing {
		return nil, fmt.Errorf("batch %s is not processing", id)
	}
	target := statePending
	if retryAt == nil {
		target = stateQuarantine
	}
	_ = os.RemoveAll(filepath.Join(s.batchPath(stateProcessing, id), "attempt"))
	if err := os.Rename(s.batchPath(stateProcessing, id), s.batchPath(target, id)); err != nil {
		return nil, fmt.Errorf("move failed batch %s: %w", id, err)
	}
	record.State = target
	record.UpdatedAt = now.UTC()
	record.LastError = cause.Error()
	record.NextAttemptAt = retryAt
	if err := s.writeRecord(record); err != nil {
		return nil, err
	}
	return cloneRecord(record), nil
}

func (s *Store) Complete(id, extractedPath string, artifact DownloadInfo, extractedSize int64, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[id]
	if !ok || record.State != stateProcessing {
		return fmt.Errorf("batch %s is not processing", id)
	}
	readyPath := s.batchPath(stateReady, id)
	if _, err := os.Lstat(readyPath); err == nil {
		return fmt.Errorf("ready batch %s already exists", id)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(extractedPath, readyPath); err != nil {
		return fmt.Errorf("publish ready batch %s: %w", id, err)
	}
	if err := syncDirectory(filepath.Join(s.root, string(stateReady))); err != nil {
		return &completionStateError{err}
	}
	record.State = stateReady
	record.UpdatedAt = now.UTC()
	record.NextAttemptAt = nil
	record.LastError = ""
	record.Artifact = &artifact
	record.ExtractedSize = extractedSize
	if err := s.writeRecord(record); err != nil {
		return &completionStateError{err}
	}
	if err := os.RemoveAll(s.batchPath(stateProcessing, id)); err != nil {
		return &completionStateError{fmt.Errorf("clean processing batch %s: %w", id, err)}
	}
	if err := syncDirectory(filepath.Join(s.root, string(stateProcessing))); err != nil {
		return &completionStateError{err}
	}
	return nil
}

func (s *Store) Record(id string) (*BatchRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[id]
	if !ok {
		return nil, false
	}
	return cloneRecord(record), true
}

func (s *Store) Counts() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	counts := map[string]int{"pending": 0, "processing": 0, "ready": 0, "quarantine": 0}
	for _, record := range s.records {
		counts[string(record.State)]++
	}
	return counts
}

func (s *Store) Writable() error {
	f, err := os.CreateTemp(filepath.Join(s.root, "state"), ".readyz-")
	if err != nil {
		return err
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	return os.Remove(name)
}

func (s *Store) writeRecord(record *BatchRecord) error {
	dir := filepath.Join(s.root, "state")
	tmp, err := os.CreateTemp(dir, "."+record.BatchID+"-")
	if err != nil {
		return fmt.Errorf("create batch state: %w", err)
	}
	name := tmp.Name()
	cleanup := func() { _ = os.Remove(name) }
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	encoder := json.NewEncoder(tmp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(record); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(name, s.statePath(record.BatchID)); err != nil {
		cleanup()
		return err
	}
	return syncDirectory(dir)
}

func writeJSONFile(path string, value any, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	if err := dir.Sync(); err != nil {
		_ = dir.Close()
		return err
	}
	return dir.Close()
}
