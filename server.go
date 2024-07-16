package main

import (
    "encoding/json"
    "fmt"
    "log"
    "net/http"
)

// Define a struct to represent the JSON payload
type GitLabPayload struct {
    Event string `json:"event"`
    Data  string `json:"data"`
}

// Handler for the /gitlab endpoint
func gitlabHandler(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
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
    fmt.Printf("Received GitLab event: %s with data: %s\n", payload.Event, payload.Data)

    // Respond with a success message
    w.WriteHeader(http.StatusOK)
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]string{"status": "success"})
}

func main() {
    // Register the /gitlab route with the handler
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("Hello, World!"))
		})
    http.HandleFunc("/gitlab", gitlabHandler)

    // Start the server
    fmt.Println("Server is running at http://localhost:8080")
    log.Fatal(http.ListenAndServe(":8080", nil))
}
