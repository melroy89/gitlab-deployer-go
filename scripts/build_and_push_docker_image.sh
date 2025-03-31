#!/usr/bin/env bash
docker build -t danger89/gitlab-deployer-go:latest -t registry.melroy.org/melroy/gitlab-artifact-deployer-go/artifact-deployer:latest .

# Publish to both GitLab Registry and Docker Hub
docker push danger89/gitlab-deployer-go:latest
docker push registry.melroy.org/melroy/gitlab-artifact-deployer-go/artifact-deployer:latest
