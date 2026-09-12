package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"time"
)

const maxWebhookBody = 1 << 20

type App struct {
	config    Config
	store     *Store
	processor *Processor
	direct    *DirectDeployer
	ctx       context.Context
}

func newApp(ctx context.Context, config Config) (*App, error) {
	downloader := newDownloader(config)
	app := &App{config: config, ctx: ctx}
	if config.Mode == "batch" {
		store, err := openStore(config.Destination)
		if err != nil {
			return nil, err
		}
		app.store = store
		app.processor = newProcessor(config, store, downloader)
	} else {
		app.direct = &DirectDeployer{config: config, downloader: downloader}
	}
	return app, nil
}

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", a.rootHandler)
	mux.HandleFunc("/gitlab", a.gitlabHandler)
	mux.HandleFunc("/healthz", a.healthHandler)
	mux.HandleFunc("/readyz", a.readyHandler)
	return mux
}

func (a *App) rootHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write([]byte("Welcome at GitLab Artifact Deployer!"))
}

func (a *App) healthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (a *App) readyHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if a.config.Mode == "direct" {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "mode": "direct"})
		return
	}
	counts, err := a.processor.Status()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not_ready", "mode": "batch", "counts": counts})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "mode": "batch", "counts": counts})
}

func (a *App) gitlabHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	provided := sha256.Sum256([]byte(r.Header.Get("X-Gitlab-Token")))
	expected := sha256.Sum256([]byte(a.config.Secret))
	if subtle.ConstantTimeCompare(provided[:], expected[:]) != 1 {
		writeError(w, http.StatusUnauthorized, "invalid_token")
		return
	}
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxWebhookBody))
	var payload GitLabPayload
	if err := decoder.Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	if payload.ObjectKind != "deployment" {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ignored", "reason": "not_deployment"})
		return
	}
	if payload.Status != "success" {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ignored", "reason": "deployment_not_successful"})
		return
	}
	if a.config.Environment != "" && payload.Environment != a.config.Environment {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ignored", "reason": "environment_mismatch"})
		return
	}
	projectID := payload.Project.ID
	if a.config.Mode == "direct" && a.config.ProjectID != 0 {
		projectID = a.config.ProjectID
	}
	if projectID < 1 || (!a.config.UseJobName && payload.DeployableID < 1) || (a.config.Mode == "batch" && payload.DeploymentID < 1) {
		writeError(w, http.StatusBadRequest, "invalid_deployment_identity")
		return
	}
	if a.config.Mode == "batch" && !fullCommitSHA.MatchString(deploymentSHA(payload)) {
		writeError(w, http.StatusBadRequest, "invalid_commit_identity")
		return
	}
	if a.config.Mode == "direct" {
		go a.direct.Deploy(a.ctx, payload)
		writeJSON(w, http.StatusOK, map[string]any{"status": "accepted", "mode": "direct"})
		return
	}
	record, created, err := a.store.Accept(payload, time.Now())
	if err != nil {
		log.Printf("persist deployment event: %v", err)
		if errors.Is(err, errBatchConflict) {
			writeError(w, http.StatusConflict, "batch_conflict")
			return
		}
		writeError(w, http.StatusInternalServerError, "persistence_failed")
		return
	}
	if !created {
		writeJSON(w, http.StatusOK, map[string]any{"status": "duplicate", "batch_id": record.BatchID, "state": record.State})
		return
	}
	log.Printf("batch=%s state=pending", record.BatchID)
	a.processor.Notify()
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "accepted", "batch_id": record.BatchID})
}

func writeError(w http.ResponseWriter, status int, reason string) {
	writeJSON(w, status, map[string]any{"status": "error", "reason": reason})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
