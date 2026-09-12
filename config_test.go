package main

import (
	"strings"
	"testing"
)

func configFrom(values map[string]string) (Config, error) {
	return readConfig(func(key string) string { return values[key] })
}

func TestConfigDefaultsPreserveDirectMode(t *testing.T) {
	c, err := configFrom(map[string]string{"GITLAB_SECRET_TOKEN": "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Mode != "direct" || c.Host != "gitlab.com" || c.Destination != "dest" || c.JobName != "deploy" || c.Branch != "main" {
		t.Fatalf("unexpected defaults: %#v", c)
	}
}

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name, message string
		values        map[string]string
	}{
		{"secret", "GITLAB_SECRET_TOKEN", nil},
		{"mode", "DEPLOYMENT_MODE", map[string]string{"GITLAB_SECRET_TOKEN": "x", "DEPLOYMENT_MODE": "other"}},
		{"host URL", "GITLAB_HOSTNAME", map[string]string{"GITLAB_SECRET_TOKEN": "x", "GITLAB_HOSTNAME": "https://gitlab.com"}},
		{"worker low", "WORKER_COUNT", map[string]string{"GITLAB_SECRET_TOKEN": "x", "WORKER_COUNT": "0"}},
		{"worker high", "WORKER_COUNT", map[string]string{"GITLAB_SECRET_TOKEN": "x", "WORKER_COUNT": "17"}},
		{"batch job name", "batch mode forbids", map[string]string{"GITLAB_SECRET_TOKEN": "x", "DEPLOYMENT_MODE": "batch", "USE_JOB_NAME": "yes"}},
		{"batch command", "batch mode forbids", map[string]string{"GITLAB_SECRET_TOKEN": "x", "DEPLOYMENT_MODE": "batch", "POST_DEPLOYMENT_COMMAND": "true"}},
		{"batch project", "batch mode forbids", map[string]string{"GITLAB_SECRET_TOKEN": "x", "DEPLOYMENT_MODE": "batch", "PROJECT_ID": "1"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := configFrom(test.values)
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("expected %q error, got %v", test.message, err)
			}
		})
	}
}
