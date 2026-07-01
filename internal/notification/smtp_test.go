package notification

import (
	"context"
	"net/smtp"
	"strings"
	"testing"
)

func TestNewSMTPSender_DisabledFallsBackToNoop(t *testing.T) {
	cases := []struct {
		name string
		cfg  SMTPConfig
	}{
		{"empty", SMTPConfig{}},
		{"host_only", SMTPConfig{Host: "smtp.example.com"}},
		{"from_only", SMTPConfig{From: "bot@example.com"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sender, ok := NewSMTPSender(tc.cfg)
			if ok {
				t.Fatalf("NewSMTPSender(%+v) reported enabled; want disabled", tc.cfg)
			}
			if _, isNoop := sender.(NoopSender); !isNoop {
				t.Fatalf("disabled config must return NoopSender, got %T", sender)
			}
			// The no-op must accept sends without error so the escalation path stays inert.
			if err := sender.Send(context.Background(), "ops@example.com", "subj", "body"); err != nil {
				t.Fatalf("NoopSender.Send: %v", err)
			}
		})
	}
}

func TestSMTPSender_SendBuildsMessageAndDialsConfiguredHost(t *testing.T) {
	sender, ok := NewSMTPSender(SMTPConfig{
		Host:     "smtp.example.com",
		Port:     2525,
		From:     "bot@example.com",
		User:     "bot",
		Password: "secret",
	})
	if !ok {
		t.Fatal("expected enabled sender")
	}
	s, isSMTP := sender.(*SMTPSender)
	if !isSMTP {
		t.Fatalf("expected *SMTPSender, got %T", sender)
	}

	var gotAddr, gotFrom string
	var gotTo []string
	var gotMsg []byte
	var gotAuth smtp.Auth
	s.sendFunc = func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
		gotAddr, gotAuth, gotFrom, gotTo, gotMsg = addr, a, from, to, msg
		return nil
	}

	if err := s.Send(context.Background(), "ops@example.com", "ASQS unstable", "line1\nline2"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotAddr != "smtp.example.com:2525" {
		t.Fatalf("addr = %q, want smtp.example.com:2525", gotAddr)
	}
	if gotAuth == nil {
		t.Fatal("expected PLAIN auth when user is configured")
	}
	if gotFrom != "bot@example.com" || len(gotTo) != 1 || gotTo[0] != "ops@example.com" {
		t.Fatalf("from/to = %q/%v", gotFrom, gotTo)
	}
	msg := string(gotMsg)
	for _, want := range []string{
		"From: bot@example.com\r\n",
		"To: ops@example.com\r\n",
		"Subject: ASQS unstable\r\n",
		"line1\r\nline2",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message missing %q:\n%s", want, msg)
		}
	}
}

func TestSMTPSender_DefaultsPortAndOmitsAuthWithoutUser(t *testing.T) {
	sender, ok := NewSMTPSender(SMTPConfig{Host: "mail.local", From: "bot@local"})
	if !ok {
		t.Fatal("expected enabled sender")
	}
	s := sender.(*SMTPSender)
	var gotAddr string
	var gotAuth smtp.Auth
	s.sendFunc = func(addr string, a smtp.Auth, _ string, _ []string, _ []byte) error {
		gotAddr, gotAuth = addr, a
		return nil
	}
	if err := s.Send(context.Background(), "ops@local", "s", "b"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotAddr != "mail.local:587" {
		t.Fatalf("addr = %q, want default port 587", gotAddr)
	}
	if gotAuth != nil {
		t.Fatal("expected nil auth when no user configured")
	}
}

func TestSMTPSender_SendErrors(t *testing.T) {
	sender, _ := NewSMTPSender(SMTPConfig{Host: "mail.local", From: "bot@local"})
	s := sender.(*SMTPSender)
	s.sendFunc = func(string, smtp.Auth, string, []string, []byte) error { return nil }

	if err := s.Send(context.Background(), "  ", "s", "b"); err == nil {
		t.Fatal("expected error for empty recipient")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Send(ctx, "ops@local", "s", "b"); err == nil {
		t.Fatal("expected error for cancelled context")
	}
}
