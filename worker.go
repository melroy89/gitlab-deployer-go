package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

const maxAttempts = 8

type Processor struct {
	config     Config
	store      *Store
	downloader *Downloader
	queue      chan string
	wake       chan struct{}
	queuedMu   sync.Mutex
	queued     map[string]bool
	wg         sync.WaitGroup
	running    atomic.Bool
	fatal      atomic.Bool
	now        func() time.Time
}

func newProcessor(config Config, store *Store, downloader *Downloader) *Processor {
	return &Processor{
		config: config, store: store, downloader: downloader,
		queue: make(chan string, config.Workers*2), wake: make(chan struct{}, 1),
		queued: make(map[string]bool), now: time.Now,
	}
}

func (p *Processor) Start(ctx context.Context) {
	p.running.Store(true)
	for range p.config.Workers {
		p.wg.Add(1)
		go p.worker(ctx)
	}
	p.wg.Add(1)
	go p.scheduler(ctx)
}

func (p *Processor) Wait() { p.wg.Wait() }

func (p *Processor) Notify() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (p *Processor) Ready() bool { return p.running.Load() && !p.fatal.Load() }

func (p *Processor) scheduler(ctx context.Context) {
	defer p.wg.Done()
	defer p.running.Store(false)
	ticker := time.NewTicker(p.config.ScanInterval)
	defer ticker.Stop()
	for {
		p.scheduleDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-p.wake:
		}
	}
}

func (p *Processor) scheduleDue(ctx context.Context) {
	for _, id := range p.store.Due(p.now()) {
		p.queuedMu.Lock()
		if p.queued[id] {
			p.queuedMu.Unlock()
			continue
		}
		p.queued[id] = true
		p.queuedMu.Unlock()
		select {
		case p.queue <- id:
		case <-ctx.Done():
			p.unqueue(id)
			return
		}
	}
}

func (p *Processor) unqueue(id string) {
	p.queuedMu.Lock()
	delete(p.queued, id)
	p.queuedMu.Unlock()
}

func (p *Processor) worker(ctx context.Context) {
	defer p.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-p.queue:
			p.unqueue(id)
			p.process(ctx, id)
		}
	}
}

func (p *Processor) process(ctx context.Context, id string) {
	record, err := p.store.Claim(id, p.now())
	if err != nil {
		p.supervisorError(id, err)
		return
	}
	if record == nil {
		return
	}
	log.Printf("batch=%s state=processing attempt=%d", id, record.Attempts)
	err = p.processClaimed(ctx, record)
	if err == nil {
		log.Printf("batch=%s state=ready", id)
		return
	}
	var completionErr *completionStateError
	if errors.As(err, &completionErr) {
		p.supervisorError(id, fmt.Errorf("ready handoff requires restart reconciliation: %w", err))
		return
	}
	if errors.Is(err, context.Canceled) {
		log.Printf("batch=%s interrupted; startup recovery will retry it", id)
		return
	}
	var retryAt *time.Time
	if !isPermanent(err) && record.Attempts < maxAttempts {
		next := p.now().Add(retryDelay(record.Attempts, p.config.RetryBase, p.config.RetryMax)).UTC()
		retryAt = &next
	}
	failed, stateErr := p.store.Fail(id, err, retryAt, p.now())
	if stateErr != nil {
		p.supervisorError(id, stateErr)
		return
	}
	log.Printf("batch=%s state=%s attempt=%d error=%q", id, failed.State, failed.Attempts, failed.LastError)
	p.Notify()
}

func (p *Processor) processClaimed(ctx context.Context, record *BatchRecord) error {
	processing := p.store.batchPath(stateProcessing, record.BatchID)
	attempt := filepath.Join(processing, "attempt")
	if err := os.RemoveAll(attempt); err != nil {
		return err
	}
	if err := os.Mkdir(attempt, 0700); err != nil {
		return err
	}
	artifactPath := filepath.Join(attempt, "artifact.zip")
	artifact, err := p.downloader.download(ctx, record.ProjectID, record.JobID, artifactPath)
	if err != nil {
		return err
	}
	extractedPath := filepath.Join(attempt, "extracted")
	if err := os.Mkdir(extractedPath, 0755); err != nil {
		return err
	}
	files, extractedSize, err := extractArchive(ctx, artifactPath, extractedPath, p.config, false)
	if err != nil {
		return err
	}
	manifest := BatchManifest{
		SchemaVersion: stateSchemaVersion, BatchID: record.BatchID,
		ProjectID: record.ProjectID, DeploymentID: record.DeploymentID, JobID: record.JobID,
		Environment: record.Environment, SHA: record.SHA, CommitURL: record.CommitURL,
		ProjectName: record.ProjectName, ProjectURL: record.ProjectURL, TriggeredBy: record.TriggeredBy,
		CompletedAt: p.now().UTC(), Artifact: artifact, ExtractedSize: extractedSize, Files: files,
	}
	if err := writeJSONFile(filepath.Join(extractedPath, "batch.json"), manifest, 0644); err != nil {
		return err
	}
	// Keep an inherited downstream-consumer ACL writable when this directory is
	// atomically handed off to ready/. The container's umask may otherwise
	// reduce the ACL mask to read/execute only.
	if err := os.Chmod(extractedPath, 0770); err != nil {
		return err
	}
	if err := syncDirectory(extractedPath); err != nil {
		return err
	}
	return p.store.Complete(record.BatchID, extractedPath, artifact, extractedSize, p.now())
}

func (p *Processor) supervisorError(id string, err error) {
	p.fatal.Store(true)
	log.Printf("batch=%s supervisor error: %v", id, err)
}

func retryDelay(attempt int, base, maximum time.Duration) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := base
	for i := 1; i < attempt && delay < maximum; i++ {
		if delay > maximum/2 {
			return maximum
		}
		delay *= 2
	}
	if delay > maximum {
		return maximum
	}
	return delay
}

func (p *Processor) Status() (map[string]int, error) {
	counts := p.store.Counts()
	if !p.Ready() {
		return counts, fmt.Errorf("worker supervisor is not ready")
	}
	if err := p.store.Writable(); err != nil {
		return counts, fmt.Errorf("spool is not writable: %w", err)
	}
	return counts, nil
}
