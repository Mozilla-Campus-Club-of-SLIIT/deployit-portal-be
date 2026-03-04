package cloudrun

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"strings"
	"time"
)

// EvaluateScript runs a custom bash script non-interactively via the ttyd websocket
type evalResponse struct {
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
}

// EvaluateScript runs a custom bash script non-interactively via the internal API sidecar
func EvaluateScript(labURL string, script string) (string, error) {
	if script == "" {
		return "", nil // No script to run
	}

	evalURL := labURL
	if strings.Contains(evalURL, "wss://") {
		evalURL = strings.Replace(evalURL, "wss://", "https://", 1)
	} else if strings.Contains(evalURL, "ws://") {
		evalURL = strings.Replace(evalURL, "ws://", "http://", 1)
	}

	evalURL = strings.TrimSuffix(evalURL, "/ws")
	evalURL = strings.TrimSuffix(evalURL, "/") + "/api/evaluate"

	log.Printf("[EVAL] Triggering API Evaluation at: %s", evalURL)

	payload := map[string]string{"script": script}
	jsonBody, _ := json.Marshal(payload)

	client := &http.Client{Timeout: 60 * time.Second}
	req, _ := http.NewRequest("POST", evalURL, bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("HTTP post failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API returned non-200 status: %d", resp.StatusCode)
	}

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read API response: %v", err)
	}

	var evalResp evalResponse
	if err := json.Unmarshal(bodyBytes, &evalResp); err != nil {
		return "", fmt.Errorf("failed to parse JSON response: %v", err)
	}

	log.Printf("[EVAL] API Success. Output:\n%s", evalResp.Stdout)

	// Convention: Return the last non-empty line of stdout
	lines := strings.Split(evalResp.Stdout, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" {
			return line, nil
		}
	}

	return "", nil
}
