package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func webhookRequest(t *testing.T, handler http.Handler, method, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var data []byte
	if raw, ok := body.([]byte); ok {
		data = raw
	} else {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, "/gitlab", bytes.NewReader(data))
	req.Header.Set("X-Gitlab-Token", token)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func TestWebhookValidationAndFiltering(t *testing.T) {
	c := testConfig(t.TempDir())
	c.Environment = "production"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	app, err := newApp(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	handler := app.routes()
	payload := testPayload()
	tests := []struct {
		name, method, token string
		body                any
		status              int
		contains            string
	}{
		{"method", http.MethodGet, "secret", payload, http.StatusMethodNotAllowed, "method_not_allowed"},
		{"token", http.MethodPost, "wrong", payload, http.StatusUnauthorized, "invalid_token"},
		{"json", http.MethodPost, "secret", []byte("{"), http.StatusBadRequest, "invalid_json"},
		{"trailing json", http.MethodPost, "secret", []byte("{}{}"), http.StatusBadRequest, "invalid_json"},
		{"not deployment", http.MethodPost, "secret", GitLabPayload{ObjectKind: "job"}, http.StatusOK, "not_deployment"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := webhookRequest(t, handler, test.method, test.token, test.body)
			if response.Code != test.status || !bytes.Contains(response.Body.Bytes(), []byte(test.contains)) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
	for _, status := range []string{"running", "failed", "canceled"} {
		t.Run(status, func(t *testing.T) {
			other := payload
			other.Status = status
			response := webhookRequest(t, handler, http.MethodPost, "secret", other)
			if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte("deployment_not_successful")) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
	for _, environment := range []string{"", "staging"} {
		t.Run("environment "+environment, func(t *testing.T) {
			other := payload
			other.Environment = environment
			response := webhookRequest(t, handler, http.MethodPost, "secret", other)
			if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte("environment_mismatch")) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestBatchWebhookAcceptanceAndDuplicate(t *testing.T) {
	c := testConfig(t.TempDir())
	app, err := newApp(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	payload := testPayload()
	first := webhookRequest(t, app.routes(), http.MethodPost, "secret", payload)
	if first.Code != http.StatusAccepted || !bytes.Contains(first.Body.Bytes(), []byte(batchID(payload))) {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	second := webhookRequest(t, app.routes(), http.MethodPost, "secret", payload)
	if second.Code != http.StatusOK || !bytes.Contains(second.Body.Bytes(), []byte("duplicate")) {
		t.Fatalf("second status=%d body=%s", second.Code, second.Body.String())
	}
	invalid := payload
	invalid.DeploymentID = 0
	response := webhookRequest(t, app.routes(), http.MethodPost, "secret", invalid)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid identity status=%d body=%s", response.Code, response.Body.String())
	}
	invalid = payload
	invalid.CommitURL = ""
	invalid.SHA = ""
	if response = webhookRequest(t, app.routes(), http.MethodPost, "secret", invalid); response.Code != http.StatusBadRequest || !bytes.Contains(response.Body.Bytes(), []byte("invalid_commit_identity")) {
		t.Fatalf("invalid commit status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHealthAndReadiness(t *testing.T) {
	c := testConfig(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	app, err := newApp(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	request := func(path string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		app.routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		return recorder
	}
	if response := request("/healthz"); response.Code != http.StatusOK {
		t.Fatalf("health status=%d", response.Code)
	}
	if response := request("/readyz"); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("pre-start readiness status=%d", response.Code)
	}
	app.processor.Start(ctx)
	if response := request("/readyz"); response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte("counts")) {
		t.Fatalf("readiness status=%d body=%s", response.Code, response.Body.String())
	}
	cancel()
	app.processor.Wait()
	if response := request("/readyz"); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("stopped readiness status=%d", response.Code)
	}
}

func TestUnsetEnvironmentAcceptsMissingEnvironment(t *testing.T) {
	c := testConfig(t.TempDir())
	app, err := newApp(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	payload := testPayload()
	payload.Environment = ""
	response := webhookRequest(t, app.routes(), http.MethodPost, "secret", payload)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
