package notification

import "context"

// EmailSender sends a single email (e.g. human-in-the-loop notification when max iteration reached and still unstable).
type EmailSender interface {
	Send(ctx context.Context, to, subject, body string) error
}

// NoopSender is an EmailSender that does nothing (for tests or when email is disabled).
type NoopSender struct{}

func (NoopSender) Send(ctx context.Context, to, subject, body string) error {
	return nil
}
