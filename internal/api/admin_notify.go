package api

// Admin notification helper: fire-and-forget emails to the install
// operator on events that warrant attention (new signup request,
// user finished connecting their Rivian account, etc).
//
// Target address is RIVOLT_EMAIL_OPERATOR_NOTICE_TO when set; unset, it
// is the configured From sender, so whoever the verified Resend sender
// is also receives the notifications (email.Client.OperatorAddress).
// Best-effort: failures are logged at WARN and do not fail the
// underlying request the user made.

import (
	"context"
	"log/slog"

	"github.com/apohor/rivolt/internal/email"
)

// notifyAdmin sends `subject` + `text` to the admin address derived
// from the email client (OperatorAddress). Returns nothing — the operator
// either receives the mail or sees the warn line in the logs.
func notifyAdmin(ctx context.Context, mailer *email.Client, logger *slog.Logger, subject, text string) {
	if mailer == nil {
		return
	}
	to := mailer.OperatorAddress()
	if to == "" {
		return
	}
	err := mailer.Send(ctx, email.Message{
		To:      to,
		Subject: subject,
		Text:    text,
	})
	if err != nil && logger != nil {
		logger.WarnContext(ctx, "admin notify failed",
			slog.String("subject", subject),
			slog.String("err", err.Error()),
		)
	}
}
