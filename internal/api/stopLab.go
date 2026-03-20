package api

import (
	"devops-lab-backend/internal/cloudrun"
	"devops-lab-backend/internal/db"
	"devops-lab-backend/internal/k8s"
	"encoding/json"
	"log"
	"net/http"
	"strings"
)

type StopLabRequest struct {
	SessionID      string `json:"sessionID"`
	SkipEvaluation bool   `json:"skipEvaluation"`
}

type StopLabResponse struct {
	Status string `json:"status"`
	Result string `json:"result,omitempty"`
	Output string `json:"output,omitempty"`
}

func StopLabHandler(sm *cloudrun.SessionManager, fc *db.FirestoreClient, kc *k8s.K8sClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req StopLabRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request", http.StatusBadRequest)
			return
		}

		if req.SessionID == "" {
			http.Error(w, "Missing sessionID", http.StatusBadRequest)
			return
		}

		session, ok := sm.GetSession(req.SessionID)
		if !ok {
			json.NewEncoder(w).Encode(StopLabResponse{Status: "failure", Result: "SESSION_NOT_FOUND"})
			return
		}

		var result string
		var evalOutput string

		if req.SkipEvaluation {
			log.Printf("[TERMINATION] Session: %s, User: %s - Evaluation skipped by user request.\n", req.SessionID, session.UserID)
		}

		// If there is an EndScript and user wants evaluation, evaluate it before destroying the container
		if !req.SkipEvaluation && session.EndScript != "" {
			var outstr string
			var err error
			if session.IsK8s && kc != nil {
				outstr, err = kc.EvaluateScript(r.Context(), session.Namespace, session.EndScript)
			} else {
				outstr, err = cloudrun.EvaluateScript(session.URL, session.EndScript)
			}
			// Clean up output string to avoid messy logs
			evalOutput = strings.TrimSpace(outstr)

			if evalOutput == "" && err != nil {
				evalOutput = "Evaluation System Error: " + err.Error()
				result = "ERROR"
			} else {
				// Convention: scripts print PASS or FAIL
				// We prioritize the string content over the exit code because a non-zero exit code
				// is actually EXPECTED when the script reports a FAIL.
				if strings.Contains(strings.ToUpper(evalOutput), "PASS") {
					result = "SUCCESS"
				} else if strings.Contains(strings.ToUpper(evalOutput), "FAIL") {
					result = "FAILURE"
				} else if err != nil {
					// It failed but doesn't have a clear PASS/FAIL string
					evalOutput = "Evaluation Error: " + err.Error() + "\nOutput: " + evalOutput
					result = "ERROR"
				} else {
					result = "EVALUATED"
				}
			}

			log.Printf("[EVALUATION] Session: %s, Challenge: %s, User: %s, Result: %s\n",
				req.SessionID, session.ChallengeID, session.UserID, result)
			log.Printf("[EVALUATION OUTPUT] %s\n", evalOutput)

			scoreEarned := 0
			if result == "SUCCESS" {
				scoreEarned = session.ChallengeScore
			}

			// Save the attempt to Firestore
			if fc != nil {
				err := fc.SaveAttempt(r.Context(), &db.ChallengeAttempt{
					UserID:          session.UserID,
					UserEmail:       session.UserEmail,
					UserDisplayName: session.UserDisplayName,
					ChallengeID:     session.ChallengeID,
					Result:          result,
					ScoreEarned:     scoreEarned,
					Output:          evalOutput,
				})
				if err != nil {
					log.Printf("[ERROR] Failed to save evaluation attempt to Firestore: %v\n", err)
				} else {
					log.Printf("[DATABASE] Attempt saved for user %s\n", session.UserID)
					// If successful, increment user's total score
					if result == "SUCCESS" && scoreEarned > 0 {
						err := fc.IncrementUserScore(r.Context(), session.UserID, scoreEarned)
						if err != nil {
							log.Printf("[ERROR] Failed to increment user score: %v\n", err)
						} else {
							log.Printf("[DATABASE] User %s total score incremented by %d\n", session.UserID, scoreEarned)
						}
					}
				}
			}
		}

		sm.DeleteSession(req.SessionID)
		json.NewEncoder(w).Encode(StopLabResponse{Status: "success", Result: result, Output: evalOutput})
	}
}
