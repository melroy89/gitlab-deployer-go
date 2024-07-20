package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

var format = "2006-01-02 15:04:05.000000"

// Define a struct to represent the JSON payload
type GitLabPayload struct {
	Event string `json:"event"`
	Data  string `json:"data"`
}

// Handler for the /gitlab endpoint
func gitlabHandler(w http.ResponseWriter, r *http.Request) {
	remoteIp := strings.Split(r.RemoteAddr, ":")[0]
	now1 := time.Now()
	fmt.Printf("%s - %s [%s] Incoming %s GitLab request\n", remoteIp, r.Host, now1.Format(format), r.Method)
	if r.Method != http.MethodPost {
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		return
	}

	gitlabToken := r.Header.Get("X-Gitlab-Token")
	secretToken := os.Getenv("GITLAB_SECRET_TOKEN")
	if gitlabToken != secretToken {
		http.Error(w, "Invalid secret GitLab token", http.StatusUnauthorized)
		return
	}
	var payload GitLabPayload

	// Parse the JSON body
	err := json.NewDecoder(r.Body).Decode(&payload)
	if err != nil {
		log.Println(err)
		http.Error(w, "Unable to parse the JSON", http.StatusBadRequest)
		return
	}

	// Log the received data
	now2 := time.Now()
	fmt.Printf("%s - %s [%s] Request data event: %s with payload data: %s\n", remoteIp, r.Host, now2.Format(format), payload.Event, payload.Data)

	// Respond with a success message
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "success"})
}

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}
	if os.Getenv("GITLAB_SECRET_TOKEN") == "" {
		log.Fatal("GITLAB_SECRET_TOKEN environment variable is NOT set but is required!")
	}

	// Register the /gitlab route with the handler
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Hello, World!"))
	})
	http.HandleFunc("/gitlab", gitlabHandler)

	// Start the server
	fmt.Println("Server is running at http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
