#!/usr/bin/env bash
set -euo pipefail

revision=$(git rev-parse HEAD)
dockerhub_image="danger89/gitlab-deployer-go:latest"
gitlab_image="registry.melroy.org/melroy/gitlab-artifact-deployer-go/artifact-deployer:latest"

docker build \
  --build-arg "VERSION=latest" \
  --build-arg "VCS_REF=${revision}" \
  -t "${dockerhub_image}" \
  -t "${gitlab_image}" .

# Publish to both GitLab Registry and Docker Hub
docker push "${dockerhub_image}"
docker push "${gitlab_image}"
