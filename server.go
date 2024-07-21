package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

var (
	secretToken       string
	projectIdOverride string
	useJobName        string
	jobName           string
	gitlabHost        string
	gitlabAccessToken string
	repoBranch        string
	destinationPath   string
)

var format = "2006-01-02 15:04:05.000000"
var gitlabApiPrefix = "api/v4"

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
	if r.Method != http.MethodPost {
		log.Printf("%s - %s ERROR: Invalid request method\n", remoteIp, host)
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		return
	}

	gitlabToken := r.Header.Get("X-Gitlab-Token")
	if gitlabToken != secretToken {
		log.Printf("%s - %s ERROR: Invalid secret GitLab token\n", remoteIp, host)
		http.Error(w, "Invalid secret GitLab token", http.StatusUnauthorized)
		return
	}

	var payload GitLabPayload
	// Parse the JSON body
	err := json.NewDecoder(r.Body).Decode(&payload)
	if err != nil {
		log.Printf("%s - %s ERROR: Unable to parse JSON\n", remoteIp, host)
		log.Println(err)
		http.Error(w, "Unable to parse the JSON", http.StatusBadRequest)
		return
	}
	log.Printf("%s - %s Incoming %s GitLab request\n", remoteIp, host, r.Method)

	// Only continue if the object_kind is "deployment"
	if payload.ObjectKind == "deployment" {
		projectId := payload.Project.Id
		jobId := payload.DeployableId
		// Override the project ID if the PROJECT_ID environment variable is set
		if projectIdOverride != "" {
			if id, err := strconv.Atoi(projectIdOverride); err == nil {
				projectId = id
			}
		}
		status := payload.Status
		switch status {
		case "running":
			log.Printf("%s - %s Deployment job is running, project ID: %d\n", remoteIp, host, projectId)
		case "failed":
			log.Printf("%s - %s Deployment job failed, project ID: %d\n", remoteIp, host, projectId)
		case "canceled":
			log.Printf("%s - %s Deployment job canceled, project ID: %d\n", remoteIp, host, projectId)
		case "success":
			if useJobName == "yes" {
				log.Printf("%s - %s Deployment job successful, project ID: %d, triggered by: %s. Waiting 3s before downloading...\n",
					remoteIp, host, projectId, payload.User.Name)
				go downloadArtifact(projectId, 0)
			} else {
				log.Printf("%s - %s Deployment job successful, project ID: %d, job ID: %d, triggered by: %s. Waiting 3s before downloading...\n",
					remoteIp, host, projectId, jobId, payload.User.Name)
				go downloadArtifact(projectId, jobId)
			}
		}
	}

	// Respond with OK
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "success"})
}

/**
 * Downloads the artifact from GitLab and extracts it to the destination path.
 * If jobId is 0, the job name is used to determine the job ID.
 */
func downloadArtifact(projectId int, jobId int) {
	// First sleep for 3 seconds to give GitLab time to finish the deployment job
	time.Sleep(3 * time.Second)

	url := fmt.Sprintf("https://%s/%s/projects/%d/jobs/artifacts/%s/download?job=%s", gitlabHost, gitlabApiPrefix, projectId, repoBranch, jobName)
	if jobId != 0 {
		url = fmt.Sprintf("https://%s/%s/projects/%d/jobs/%d/artifacts", gitlabHost, gitlabApiPrefix, projectId, jobId)
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		log.Printf("Error creating request: %v\n", err)
		return
	}

	if gitlabAccessToken != "" {
		req.Header.Set("PRIVATE-TOKEN", gitlabAccessToken)
	}

	client := &http.Client{}
	res, err := client.Do(req)
	if err != nil {
		log.Printf("Error making http request: %v\n", err)
		return
	}
	defer res.Body.Close() // Close the body resource when error != nil
	if res.StatusCode != http.StatusOK {
		log.Printf("downloading artifact (status code: %d), URL: %s\n", res.StatusCode, res.Request.URL)
		return
	}

	// I closed the body resource in the previous line, so we may need to move that code down..
	resBody, err := io.ReadAll(res.Body)
	if err != nil {
		log.Printf("Error reading the body data: %v.\n", err)
		return
	}

	log.Printf("Downloaded of artifact successfully, project ID: %d.\n", projectId)

	// Unzip the data from resBody
	err = unzip(resBody, destinationPath)
	if err != nil {
		log.Printf("Failed to unzip file: %v", err)
		return
	}

	log.Printf("Unzipping of artifact went succesfully, project ID: %d. Done!\n", projectId)
}

func unzip(data []byte, dest string) error {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("failed to create zip reader: %w", err)
	}

	for _, file := range reader.File {
		filePath := filepath.Join(dest, file.Name)

		// Create directories as needed
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(filePath, os.ModePerm); err != nil {
				return fmt.Errorf("failed to create directory: %w", err)
			}
			continue
		}

		// Create a file
		if err := os.MkdirAll(filepath.Dir(filePath), os.ModePerm); err != nil {
			return fmt.Errorf("failed to create directory for file: %w", err)
		}
		outFile, err := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, file.Mode())
		if err != nil {
			return fmt.Errorf("failed to open file: %w", err)
		}

		// Extract the file
		rc, err := file.Open()
		if err != nil {
			outFile.Close()
			return fmt.Errorf("failed to open zip file: %w", err)
		}
		_, err = io.Copy(outFile, rc)

		// Close the file and its reader
		outFile.Close()
		rc.Close()
		if err != nil {
			return fmt.Errorf("failed to copy file contents: %w", err)
		}
	}

	return nil
}

func main() {
	// Configure the logger to include date and time
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	// Load .env file
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}
	secretToken = os.Getenv("GITLAB_SECRET_TOKEN")
	projectIdOverride = os.Getenv("PROJECT_ID")
	useJobName = os.Getenv("USE_JOB_NAME")
	jobName = os.Getenv("JOB_NAME")
	gitlabHost = os.Getenv("GITLAB_HOSTNAME")
	gitlabAccessToken = os.Getenv("ACCESS_TOKEN")
	repoBranch = os.Getenv("REPO_BRANCH")
	destinationPath = os.Getenv("DESTINATION_PATH")

	if secretToken == "" {
		log.Fatal("GITLAB_SECRET_TOKEN environment variable is NOT set but is required!")
	}
	if gitlabHost == "" {
		gitlabHost = "gitlab.com"
	}
	if gitlabHost == "" {
		gitlabHost = "gitlab.com"
	}
	if jobName == "" {
		jobName = "deploy"
	}
	if repoBranch == "" {
		repoBranch = "main"
	}
	if destinationPath == "" {
		destinationPath = "dest"
	}

	// Register the /gitlab route with the handler
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Hello, World!"))
	})
	http.HandleFunc("/gitlab", gitlabHandler)

	// Start the server
	log.Println("INFO: Server is running at: http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
