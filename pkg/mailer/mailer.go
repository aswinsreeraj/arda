package mailer

import (
	"fmt"
	"log"
	"net/smtp"
	"os"
	"strconv"
	"strings"
)

// Config holds connection and template parameters for Mailtrap email delivery.
type Config struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	AppURL   string
}

// LoadConfigFromEnv reads Mailtrap configuration from environment variables with sensible defaults.
func LoadConfigFromEnv() Config {
	host := strings.TrimSpace(os.Getenv("MAILTRAP_HOST"))
	if host == "" {
		host = "sandbox.smtp.mailtrap.io"
	}

	port := 2525
	if pStr := strings.TrimSpace(os.Getenv("MAILTRAP_PORT")); pStr != "" {
		if p, err := strconv.Atoi(pStr); err == nil && p > 0 {
			port = p
		}
	}

	user := strings.TrimSpace(os.Getenv("MAILTRAP_USER"))
	if user == "" {
		user = strings.TrimSpace(os.Getenv("MAILTRAP_USERNAME"))
	}

	pass := strings.TrimSpace(os.Getenv("MAILTRAP_PASS"))
	if pass == "" {
		pass = strings.TrimSpace(os.Getenv("MAILTRAP_PASSWORD"))
	}

	from := strings.TrimSpace(os.Getenv("MAILTRAP_FROM"))
	if from == "" {
		from = "no-reply@arda.security"
	}

	appURL := strings.TrimSpace(os.Getenv("APP_URL"))
	if appURL == "" {
		appURL = strings.TrimSpace(os.Getenv("FRONTEND_URL"))
	}
	if appURL == "" {
		appURL = "http://localhost:5174"
	}

	return Config{
		Host:     host,
		Port:     port,
		Username: user,
		Password: pass,
		From:     from,
		AppURL:   strings.TrimRight(appURL, "/"),
	}
}

// Client wraps Mailtrap SMTP delivery.
type Client struct {
	config Config
}

// NewClient initializes a new Mailtrap mailer client.
func NewClient(cfg Config) *Client {
	return &Client{config: cfg}
}

// IsConfigured returns true if Mailtrap credentials are set.
func (c *Client) IsConfigured() bool {
	return c.config.Username != "" && c.config.Password != ""
}

// AppURL returns the configured base frontend application URL.
func (c *Client) AppURL() string {
	return c.config.AppURL
}

// SendRaw dispatches an email message using Mailtrap SMTP.
// If Mailtrap credentials are not configured, it logs the outgoing email for local inspection without failing.
func (c *Client) SendRaw(to, subject, htmlBody string) error {
	if !c.IsConfigured() {
		log.Printf("[MAILTRAP DEV] (No Mailtrap credentials configured - printed to log)\nTo: %s\nSubject: %s\n%s\n", to, subject, htmlBody)
		return nil
	}

	addr := fmt.Sprintf("%s:%d", c.config.Host, c.config.Port)
	auth := smtp.PlainAuth("", c.config.Username, c.config.Password, c.config.Host)

	msg := []byte(fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n%s",
		c.config.From, to, subject, htmlBody))

	if err := smtp.SendMail(addr, auth, c.config.From, []string{to}, msg); err != nil {
		return fmt.Errorf("failed to send email via Mailtrap (%s): %w", addr, err)
	}

	log.Printf("[MAILTRAP] Successfully dispatched email to %s (Subject: %s)", to, subject)
	return nil
}

// SendInvitationEmail sends a branded team invitation email with an activation link.
func (c *Client) SendInvitationEmail(to, inviterName, orgName, inviteLink string) error {
	subject := fmt.Sprintf("You have been invited to join %s on ARDA", orgName)
	html := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head><meta charset="utf-8"><title>%s</title></head>
<body style="font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Helvetica, Arial, sans-serif; background-color: #0c0c0c; color: #ffffff; padding: 40px 20px;">
  <div style="max-width: 540px; margin: 0 auto; background: #161616; border: 1px solid #2a2a2a; border-radius: 12px; padding: 32px; box-shadow: 0 4px 20px rgba(0,0,0,0.5);">
    <div style="font-size: 18px; font-weight: bold; letter-spacing: 0.1em; color: #ffffff; margin-bottom: 24px;">ΛRDΛ <span style="font-size: 12px; color: #3b82f6; font-weight: normal; margin-left: 8px;">SECURITY &amp; GOVERNANCE</span></div>
    <h2 style="font-size: 20px; font-weight: 600; margin: 0 0 12px 0; color: #f3f4f6;">Join %s on ARDA</h2>
    <p style="color: #9ca3af; font-size: 14px; line-height: 1.6; margin: 0 0 24px 0;">
      <strong>%s</strong> has invited you to collaborate on the <strong>%s</strong> agent governance and reliability workspace.
    </p>
    <div style="text-align: center; margin: 32px 0;">
      <a href="%s" style="background-color: #2563eb; color: #ffffff; padding: 12px 28px; border-radius: 8px; text-decoration: none; font-weight: 500; font-size: 14px; display: inline-block;">Accept Invitation &rarr;</a>
    </div>
    <p style="color: #6b7280; font-size: 12px; line-height: 1.5; margin: 24px 0 0 0; border-top: 1px solid #262626; pt: 16px;">
      If the button above does not work, copy and paste this link into your browser:<br>
      <a href="%s" style="color: #3b82f6; word-break: break-all;">%s</a>
    </p>
  </div>
</body>
</html>`, subject, orgName, inviterName, orgName, inviteLink, inviteLink, inviteLink)

	return c.SendRaw(to, subject, html)
}

// SendVerificationOTPEmail sends an OTP verification code and confirmation link for email verification.
func (c *Client) SendVerificationOTPEmail(to, otpCode, confirmLink string) error {
	subject := fmt.Sprintf("Your ARDA Verification Code: %s", otpCode)
	html := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head><meta charset="utf-8"><title>%s</title></head>
<body style="font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Helvetica, Arial, sans-serif; background-color: #0c0c0c; color: #ffffff; padding: 40px 20px;">
  <div style="max-width: 540px; margin: 0 auto; background: #161616; border: 1px solid #2a2a2a; border-radius: 12px; padding: 32px; box-shadow: 0 4px 20px rgba(0,0,0,0.5);">
    <div style="font-size: 18px; font-weight: bold; letter-spacing: 0.1em; color: #ffffff; margin-bottom: 24px;">ΛRDΛ <span style="font-size: 12px; color: #3b82f6; font-weight: normal; margin-left: 8px;">SECURITY &amp; GOVERNANCE</span></div>
    <h2 style="font-size: 20px; font-weight: 600; margin: 0 0 12px 0; color: #f3f4f6;">Verify Your Email Address</h2>
    <p style="color: #9ca3af; font-size: 14px; line-height: 1.6; margin: 0 0 20px 0;">
      Use the one-time passcode (OTP) below to complete your registration, or click the confirmation button. This code is valid for 15 minutes.
    </p>
    <div style="background: #101010; border: 1px dashed #3b82f6; border-radius: 8px; padding: 18px; text-align: center; margin: 24px 0;">
      <span style="font-family: monospace; font-size: 32px; font-weight: bold; letter-spacing: 0.25em; color: #60a5fa;">%s</span>
    </div>
    <div style="text-align: center; margin: 28px 0 16px 0;">
      <a href="%s" style="background-color: #2563eb; color: #ffffff; padding: 12px 28px; border-radius: 8px; text-decoration: none; font-weight: 500; font-size: 14px; display: inline-block;">Confirm Email Address &rarr;</a>
    </div>
    <p style="color: #6b7280; font-size: 12px; line-height: 1.5; margin: 24px 0 0 0; border-top: 1px solid #262626; padding-top: 16px;">
      Direct link: <a href="%s" style="color: #3b82f6; word-break: break-all;">%s</a>
    </p>
  </div>
</body>
</html>`, subject, otpCode, confirmLink, confirmLink, confirmLink)

	return c.SendRaw(to, subject, html)
}
