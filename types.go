package main

import "time"

const stateSchemaVersion = 1

type GitLabPayload struct {
	ObjectKind   string `json:"object_kind"`
	Status       string `json:"status"`
	DeploymentID int64  `json:"deployment_id"`
	DeployableID int64  `json:"deployable_id"`
	Environment  string `json:"environment"`
	Project      struct {
		ID     int64  `json:"id"`
		Name   string `json:"name"`
		WebURL string `json:"web_url"`
	} `json:"project"`
	SHA         string `json:"sha"`
	ShortSHA    string `json:"short_sha"`
	CommitURL   string `json:"commit_url"`
	CommitTitle string `json:"commit_title"`
	User        struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		Username string `json:"username"`
	} `json:"user"`
}

type BatchState string

const (
	statePending    BatchState = "pending"
	stateProcessing BatchState = "processing"
	stateReady      BatchState = "ready"
	stateQuarantine BatchState = "quarantine"
)

type BatchRecord struct {
	SchemaVersion int           `json:"schema_version"`
	BatchID       string        `json:"batch_id"`
	State         BatchState    `json:"state"`
	ProjectID     int64         `json:"project_id"`
	DeploymentID  int64         `json:"deployment_id"`
	JobID         int64         `json:"job_id"`
	Environment   string        `json:"environment"`
	SHA           string        `json:"sha,omitempty"`
	CommitURL     string        `json:"commit_url,omitempty"`
	ProjectName   string        `json:"project_name,omitempty"`
	ProjectURL    string        `json:"project_url,omitempty"`
	TriggeredBy   string        `json:"triggered_by,omitempty"`
	Attempts      int           `json:"attempts"`
	AcceptedAt    time.Time     `json:"accepted_at"`
	UpdatedAt     time.Time     `json:"updated_at"`
	NextAttemptAt *time.Time    `json:"next_attempt_at,omitempty"`
	LastError     string        `json:"last_error,omitempty"`
	Artifact      *DownloadInfo `json:"artifact,omitempty"`
	ExtractedSize int64         `json:"extracted_bytes,omitempty"`
}

type BatchManifest struct {
	SchemaVersion int             `json:"schema_version"`
	BatchID       string          `json:"batch_id"`
	ProjectID     int64           `json:"project_id"`
	DeploymentID  int64           `json:"deployment_id"`
	JobID         int64           `json:"job_id"`
	Environment   string          `json:"environment"`
	SHA           string          `json:"sha,omitempty"`
	CommitURL     string          `json:"commit_url,omitempty"`
	ProjectName   string          `json:"project_name,omitempty"`
	ProjectURL    string          `json:"project_url,omitempty"`
	TriggeredBy   string          `json:"triggered_by,omitempty"`
	CompletedAt   time.Time       `json:"completed_at"`
	Artifact      DownloadInfo    `json:"artifact"`
	ExtractedSize int64           `json:"extracted_bytes"`
	Files         []InventoryFile `json:"files"`
}
