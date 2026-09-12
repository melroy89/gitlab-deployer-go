package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	Mode, Environment, Secret, Host, AccessToken   string
	Destination, TempDir, Command, CommandCWD      string
	Branch, JobName, ListenAddress                 string
	ProjectID                                      int64
	UseJobName                                     bool
	Workers, MaxFiles                              int
	MaxArtifact, MaxExtracted                      int64
	RetryBase, RetryMax, ScanInterval, DirectDelay time.Duration
}

func loadDotEnv() error {
	err := godotenv.Load()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func readConfig(get func(string) string) (Config, error) {
	value := func(key, fallback string) string {
		if s := get(key); s != "" {
			return s
		}
		return fallback
	}
	c := Config{
		Mode: value("DEPLOYMENT_MODE", "direct"), Environment: get("DEPLOYMENT_ENVIRONMENT"),
		Secret: get("GITLAB_SECRET_TOKEN"), Host: value("GITLAB_HOSTNAME", "gitlab.com"), AccessToken: get("ACCESS_TOKEN"),
		Destination: value("DESTINATION_PATH", "dest"), TempDir: get("TEMP_DIR"),
		Command: get("POST_DEPLOYMENT_COMMAND"), CommandCWD: get("POST_DEPLOYMENT_CWD"),
		Branch: value("REPO_BRANCH", "main"), JobName: value("JOB_NAME", "deploy"), UseJobName: get("USE_JOB_NAME") == "yes",
		ListenAddress: value("LISTEN_ADDRESS", ":3042"), RetryBase: 5 * time.Second, RetryMax: 15 * time.Minute,
		ScanInterval: time.Second, DirectDelay: 3 * time.Second,
	}
	if c.Secret == "" {
		return c, errors.New("GITLAB_SECRET_TOKEN is required")
	}
	if c.Mode != "direct" && c.Mode != "batch" {
		return c, errors.New("DEPLOYMENT_MODE must be direct or batch")
	}
	u, err := url.Parse("https://" + c.Host)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || strings.Contains(c.Host, "://") {
		return c, errors.New("GITLAB_HOSTNAME must be a hostname with optional port")
	}
	if c.CommandCWD == "" {
		c.CommandCWD = c.Destination
	}
	if s := get("PROJECT_ID"); s != "" {
		c.ProjectID, err = strconv.ParseInt(s, 10, 64)
		if err != nil || c.ProjectID < 1 {
			return c, errors.New("PROJECT_ID must be a positive integer")
		}
	}
	if c.Mode == "batch" && (c.UseJobName || c.Command != "" || c.ProjectID != 0) {
		return c, errors.New("batch mode forbids USE_JOB_NAME=yes, POST_DEPLOYMENT_COMMAND, and PROJECT_ID overrides")
	}
	if c.ListenAddress == "" {
		return c, errors.New("LISTEN_ADDRESS must not be empty")
	}
	number := func(key string, fallback, max int64) (int64, error) {
		n, err := strconv.ParseInt(value(key, strconv.FormatInt(fallback, 10)), 10, 64)
		if err != nil || n < 1 || n > max {
			return 0, fmt.Errorf("%s must be between 1 and %d", key, max)
		}
		return n, nil
	}
	n, err := number("WORKER_COUNT", 2, 16)
	if err != nil {
		return c, err
	}
	c.Workers = int(n)
	// Leave room for the one-byte overrun probe used by LimitedReader.
	c.MaxArtifact, err = number("MAX_ARTIFACT_BYTES", 1<<30, 1<<62)
	if err != nil {
		return c, err
	}
	c.MaxExtracted, err = number("MAX_EXTRACTED_BYTES", 2<<30, 1<<62)
	if err != nil {
		return c, err
	}
	n, err = number("MAX_ARTIFACT_FILES", 1000, 1000000)
	if err != nil {
		return c, err
	}
	c.MaxFiles = int(n)
	return c, nil
}
