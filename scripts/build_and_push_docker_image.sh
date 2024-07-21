#!/usr/bin/env bash
docker build -t danger89/gitlab-deployer-go .
docker push danger89/gitlab-deployer-go:latest
