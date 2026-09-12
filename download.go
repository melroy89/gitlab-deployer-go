package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

var errArtifactRedirectPolicy = errors.New("artifact redirect rejected")

type permanentError struct{ reason string }

func (e *permanentError) Error() string { return e.reason }
func permanent(reason string) error     { return &permanentError{reason} }
func isPermanent(err error) bool        { var p *permanentError; return errors.As(err, &p) }

type Downloader struct {
	config  Config
	client  *http.Client
	baseURL string
}
type DownloadInfo struct {
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

func newDownloader(c Config) *Downloader {
	return &Downloader{config: c, baseURL: "https://" + c.Host, client: &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errArtifactRedirectPolicy
			}
			if req.URL.Scheme != "https" {
				return errArtifactRedirectPolicy
			}
			// GitLab can redirect to object storage. Never forward credentials there,
			// including when a later redirect returns to the original origin.
			for _, prev := range via {
				if req.URL.Host != prev.URL.Host || req.URL.Scheme != prev.URL.Scheme {
					req.Header.Del("PRIVATE-TOKEN")
					req.Header.Del("Authorization")
					req.Header.Del("Cookie")
				}
			}
			return nil
		},
	}}
}

func (d *Downloader) artifactURL(project, job int64) string {
	base := fmt.Sprintf("%s/api/v4/projects/%d/jobs/", d.baseURL, project)
	if d.config.Mode == "direct" && d.config.UseJobName {
		return base + "artifacts/" + url.PathEscape(d.config.Branch) + "/download?job=" + url.QueryEscape(d.config.JobName)
	}
	return fmt.Sprintf("%s%d/artifacts", base, job)
}

func (d *Downloader) download(ctx context.Context, project, job int64, filename string) (DownloadInfo, error) {
	var info DownloadInfo
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.artifactURL(project, job), nil)
	if err != nil {
		return info, permanent("invalid artifact request")
	}
	if d.config.AccessToken != "" {
		req.Header.Set("PRIVATE-TOKEN", d.config.AccessToken)
	}
	res, err := d.client.Do(req)
	if err != nil {
		if errors.Is(err, errArtifactRedirectPolicy) {
			return info, permanent("artifact redirect rejected")
		}
		// url.Error can contain signed object-storage URLs. Never persist/log it.
		return info, errors.New("artifact network request failed")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		reason := fmt.Sprintf("artifact HTTP status %d", res.StatusCode)
		if res.StatusCode == 408 || res.StatusCode == 429 || res.StatusCode >= 500 {
			return info, errors.New(reason)
		}
		return info, permanent(reason)
	}
	if res.ContentLength > d.config.MaxArtifact {
		return info, permanent("compressed artifact size limit exceeded")
	}
	f, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return info, errors.New("cannot create artifact file")
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(res.Body, d.config.MaxArtifact+1))
	syncErr := f.Sync()
	closeErr := f.Close()
	if n > d.config.MaxArtifact {
		return info, permanent("compressed artifact size limit exceeded")
	}
	if copyErr != nil {
		return info, errors.New("artifact stream interrupted")
	}
	if syncErr != nil || closeErr != nil {
		return info, errors.New("cannot persist artifact file")
	}
	if err := res.Body.Close(); err != nil {
		return info, errors.New("cannot close artifact response")
	}
	return DownloadInfo{SHA256: fmt.Sprintf("%x", h.Sum(nil)), Bytes: n}, nil
}
