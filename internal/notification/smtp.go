package notification

import (
	"context"
	"fmt"
	"net/smtp"
	"strings"
)

// SMTPConfig configures an SMTPSender. Host and From are the minimum required fields; Port
// defaults to 587. When User is set, PLAIN auth (with STARTTLS when the server advertises it) is
// used via net/smtp.SendMail.
type SMTPConfig struct {
	Host     string
	Port     int
	From     string
	User     string
	Password string
}

// Enabled reports whether the config has the minimum fields to send mail.
func (c SMTPConfig) Enabled() bool {
	return strings.TrimSpace(c.Host) != "" && strings.TrimSpace(c.From) != ""
}

// SMTPSender sends a single notification email over SMTP. The unexported sendFunc seam lets tests
// substitute the transport without a live server.
type SMTPSender struct {
	cfg      SMTPConfig
	sendFunc func(addr string, a smtp.Auth, from string, to []string, msg []byte) error
}

// NewSMTPSender constructs an SMTPSender from cfg. When cfg is not Enabled it returns
// (NoopSender{}, false) so callers can keep the no-op behavior without branching on nil.
func NewSMTPSender(cfg SMTPConfig) (EmailSender, bool) {
	if !cfg.Enabled() {
		return NoopSender{}, false
	}
	if cfg.Port == 0 {
		cfg.Port = 587
	}
	return &SMTPSender{cfg: cfg, sendFunc: smtp.SendMail}, true
}

// Send delivers one plain-text message to a single recipient.
func (s *SMTPSender) Send(ctx context.Context, to, subject, body string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(to) == "" {
		return fmt.Errorf("notification: empty recipient")
	}
	var auth smtp.Auth
	if s.cfg.User != "" {
		auth = smtp.PlainAuth("", s.cfg.User, s.cfg.Password, s.cfg.Host)
	}
	send := s.sendFunc
	if send == nil {
		send = smtp.SendMail
	}
	addr := fmt.Sprintf("%s:%d", s.cfg.Host, s.cfg.Port)
	return send(addr, auth, s.cfg.From, []string{to}, buildMessage(s.cfg.From, to, subject, body))
}

// buildMessage renders a minimal RFC 5322 plain-text message with CRLF line endings.
func buildMessage(from, to, subject, body string) []byte {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Subject: " + subject + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	return []byte(b.String())
}

var _ EmailSender = (*SMTPSender)(nil)
