package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

var (
	secretToken       string
	projectIdOverride string
	useJobName        string
)

var format = "2006-01-02 15:04:05.000000"

type GitLabPayload struct {
	ObjectKind             string `json:"object_kind"`
	Status                 string `json:"status"`
	DeploymentId           int    `json:"deployment_id"`
	DeployableId           int    `json:"deployable_id"`
	DeployableUrl          string `json:"deployable_url"`
	Environment            string `json:"environment"`
	EnvironmentExternalUrl string `json:"environment_external_url"`
	Project                struct {
		Id     int    `json:"id"`
		Name   string `json:"name"`
		WebUrl string `json:"web_url"`
	} `json:"project"`
	ShortSha string `json:"short_sha"`
	User     struct {
		Id        int    `json:"id"`
		Name      string `json:"name"`
		Username  string `json:"username"`
		AvatarUrl string `json:"avatar_url"`
		Email     string `json:"email"`
	} `json:"user"`
	CommitUrl   string `json:"commit_url"`
	CommitTitle string `json:"commit_title"`
}

// Handler for the /gitlab endpoint
func gitlabHandler(w http.ResponseWriter, r *http.Request) {
	remoteIp := strings.Split(r.RemoteAddr, ":")[0]
	host := strings.Split(r.Host, ":")[0]
	now1 := time.Now()
	if r.Method != http.MethodPost {
		fmt.Printf("%s - %s [%s] ERROR: Invalid request method\n", remoteIp, host, now1.Format(format))
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		return
	}

	gitlabToken := r.Header.Get("X-Gitlab-Token")
	if gitlabToken != secretToken {
		fmt.Printf("%s - %s [%s] ERROR: Invalid secret GitLab token\n", remoteIp, host, now1.Format(format))
		http.Error(w, "Invalid secret GitLab token", http.StatusUnauthorized)
		return
	}
	var payload GitLabPayload

	// Parse the JSON body
	err := json.NewDecoder(r.Body).Decode(&payload)
	if err != nil {
		fmt.Printf("%s - %s [%s] ERROR: Unable to parse JSON\n", remoteIp, host, now1.Format(format))
		log.Println(err)
		http.Error(w, "Unable to parse the JSON", http.StatusBadRequest)
		return
	}
	fmt.Printf("%s - %s [%s] Incoming %s GitLab request\n", remoteIp, host, now1.Format(format), r.Method)

	// Only continue if the object_kind is "deployment"
	if payload.ObjectKind == "deployment" {
		now2 := time.Now()
		projectId := payload.Project.Id
		// Override the project ID if the PROJECT_ID environment variable is set
		if projectIdOverride != "" {
			if id, err := strconv.Atoi(projectIdOverride); err == nil {
				projectId = id
			}
		}
		status := payload.Status
		switch status {
		case "running":
			fmt.Printf("%s - %s [%s] Deployment job is running, project ID: %d\n", remoteIp, host, now2.Format(format), projectId)
		case "failed":
			fmt.Printf("%s - %s [%s] Deployment job failed, project ID: %d\n", remoteIp, host, now2.Format(format), projectId)
		case "canceled":
			fmt.Printf("%s - %s [%s] Deployment job canceled, project ID: %d\n", remoteIp, host, now2.Format(format), projectId)
		case "success":
			fmt.Printf("%s - %s [%s] Deployment job successful, project ID: %d, triggered by: %s. Waiting 3s before downloading...\n",
				remoteIp, host, now2.Format(format), projectId, payload.User.Name)
		}
	}
	// Respond with OK
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "success"})
}

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}
	secretToken = os.Getenv("GITLAB_SECRET_TOKEN")
	projectIdOverride = os.Getenv("PROJECT_ID")
	useJobName = os.Getenv("USE_JOB_NAME")

	if secretToken == "" {
		log.Fatal("GITLAB_SECRET_TOKEN environment variable is NOT set but is required!")
	}

	// Register the /gitlab route with the handler
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Hello, World!"))
	})
	http.HandleFunc("/gitlab", gitlabHandler)

	// Start the server
	fmt.Println("INFO: Server is running at: http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
