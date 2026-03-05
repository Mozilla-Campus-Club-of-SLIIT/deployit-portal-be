package api

import (
	"devops-lab-backend/internal/db"
	"encoding/json"
	"fmt"
	"net/http"
	"net/smtp"
	"os"
)

type ForgotPasswordRequest struct {
	Email string `json:"email"`
}

type SendVerificationRequest struct {
	Email string `json:"email"`
}

func sendEmail(to, subject, body string) error {
	smtpHost := os.Getenv("SMTP_HOST")
	smtpPort := os.Getenv("SMTP_PORT")
	smtpUser := os.Getenv("SMTP_USER")
	smtpPass := os.Getenv("SMTP_PASS")
	smtpFrom := os.Getenv("SMTP_FROM")

	if smtpHost == "" || smtpPort == "" {
		return fmt.Errorf("SMTP configuration is incomplete")
	}
	if smtpFrom == "" {
		smtpFrom = "noreply@deployit.local"
	}

	auth := smtp.PlainAuth("", smtpUser, smtpPass, smtpHost)
	address := smtpHost + ":" + smtpPort

	msg := []byte("To: " + to + "\r\n" +
		"From: " + smtpFrom + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"Content-Type: text/html; charset=UTF-8\r\n\r\n" +
		body + "\r\n")

	// If no auth is provided, we can connect without auth (some local/dev servers)
	if smtpUser == "" {
		c, err := smtp.Dial(address)
		if err != nil {
			return err
		}
		defer c.Close()
		if err = c.Mail(smtpFrom); err != nil {
			return err
		}
		if err = c.Rcpt(to); err != nil {
			return err
		}
		w, err := c.Data()
		if err != nil {
			return err
		}
		_, err = w.Write(msg)
		if err != nil {
			return err
		}
		err = w.Close()
		if err != nil {
			return err
		}
		return c.Quit()
	}

	return smtp.SendMail(address, auth, smtpFrom, []string{to}, msg)
}

func ForgotPasswordHandler(fc *db.FirestoreClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req ForgotPasswordRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}

		if req.Email == "" {
			http.Error(w, "Email is required", http.StatusBadRequest)
			return
		}

		user, err := fc.GetUserByEmail(r.Context(), req.Email)
		if err != nil {
			// Do not leak that the user doesn't exist. Just say success.
			w.WriteHeader(http.StatusOK)
			return
		}

		// Generate a reset token (in a real app, save to DB with expiration)
		// Here we just send a placeholder link for demonstration.
		resetLink := "http://localhost:3000/reset-password?token=mockstoken123"

		subject := "Deploy(it) - Password Reset"
		body := fmt.Sprintf(`
			<h2>Password Reset Request</h2>
			<p>Hello %s,</p>
			<p>We received a request to reset your password for your Deploy(it) account.</p>
			<p>Click the link below to reset your password:</p>
			<a href="%s">Reset Password</a>
			<p>If you didn't request this, you can ignore this email.</p>
		`, user.DisplayName, resetLink)

		if err := sendEmail(req.Email, subject, body); err != nil {
			fmt.Printf("Error sending email: %%v\n", err)
			http.Error(w, "Failed to send email. Check SMTP config.", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
	}
}

func SendVerificationHandler(fc *db.FirestoreClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req SendVerificationRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}

		if req.Email == "" {
			http.Error(w, "Email is required", http.StatusBadRequest)
			return
		}

		// We assume the caller is authenticated and sent their own email,
		// but since it could be from unverified state, we just send it if the account exists.
		user, err := fc.GetUserByEmail(r.Context(), req.Email)
		if err != nil {
			http.Error(w, "User not found", http.StatusNotFound)
			return
		}

		verifyLink := "http://localhost:3000/verify-email?token=verifytoken123"

		subject := "Deploy(it) - Verify Your Email"
		body := fmt.Sprintf(`
			<h2>Verify Your Email</h2>
			<p>Hello %s,</p>
			<p>Welcome to Deploy(it)! Please verify your email address to unlock full access to the platform.</p>
			<p>Click the link below to verify:</p>
			<a href="%s">Verify Email</a>
		`, user.DisplayName, verifyLink)

		if err := sendEmail(req.Email, subject, body); err != nil {
			fmt.Printf("Error sending verification email: %%v\n", err)
			http.Error(w, "Failed to send verification email. Check SMTP config.", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
	}
}
