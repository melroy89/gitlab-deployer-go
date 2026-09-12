package main

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

type DirectDeployer struct {
	config     Config
	downloader *Downloader
	mu         sync.Mutex
}

func (d *DirectDeployer) Deploy(ctx context.Context, payload GitLabPayload) {
	if d.config.DirectDelay > 0 {
		timer := time.NewTimer(d.config.DirectDelay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	projectID := payload.Project.ID
	if d.config.ProjectID != 0 {
		projectID = d.config.ProjectID
	}
	jobID := payload.DeployableID
	privateTemp, err := os.MkdirTemp(d.config.TempDir, "artifact-deployer-")
	if err != nil {
		log.Printf("direct project=%d error=%q", projectID, "cannot create temporary directory")
		return
	}
	defer os.RemoveAll(privateTemp)
	artifactPath := filepath.Join(privateTemp, "artifact.zip")
	if _, err = d.downloader.download(ctx, projectID, jobID, artifactPath); err != nil {
		log.Printf("direct project=%d job=%d error=%q", projectID, jobID, err.Error())
		return
	}
	if err = os.MkdirAll(d.config.Destination, 0755); err != nil {
		log.Printf("direct project=%d error=%q", projectID, "cannot create destination")
		return
	}
	if _, _, err = extractArchive(ctx, artifactPath, d.config.Destination, d.config, true); err != nil {
		log.Printf("direct project=%d error=%q", projectID, err.Error())
		return
	}
	if d.config.Command != "" {
		command := exec.CommandContext(ctx, "bash", "-c", d.config.Command)
		command.Dir = d.config.CommandCWD
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		if err := command.Run(); err != nil {
			log.Printf("direct project=%d post-deployment error=%q", projectID, err.Error())
			return
		}
	}
	log.Printf("direct project=%d job=%d deployment complete", projectID, jobID)
}
