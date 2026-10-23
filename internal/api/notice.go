package api

import (
	"fmt"
	"strings"
	"time"
)

// TransferNotice is the operator-transfer notice window (Terms §11 "Transfer" and
// "Changes": 30 days' notice by email and in the app). From is the day the notice
// email goes out, Date the day ZeroDowntime LLC takes over; both are whole UTC days
// from $RIVOLT_TRANSFER_NOTICE_FROM and $RIVOLT_TRANSFER_DATE (YYYY-MM-DD). The
// zero value shows nothing.
type TransferNotice struct {
	From time.Time
	Date time.Time
}

// ParseTransferNotice reads the two configured days. Both empty is "no notice"; one
// without the other, a bad day, or a Date not after From is an error (the caller
// logs it and runs without a notice).
func ParseTransferNotice(from, date string) (TransferNotice, error) {
	from, date = strings.TrimSpace(from), strings.TrimSpace(date)
	if from == "" && date == "" {
		return TransferNotice{}, nil
	}
	f, err1 := time.Parse("2006-01-02", from)
	d, err2 := time.Parse("2006-01-02", date)
	if err1 != nil || err2 != nil || !d.After(f) {
		return TransferNotice{}, fmt.Errorf("transfer notice: RIVOLT_TRANSFER_NOTICE_FROM and RIVOLT_TRANSFER_DATE must be two YYYY-MM-DD days, the second after the first")
	}
	return TransferNotice{From: f, Date: d}, nil
}

// siteNotice is the in-app notice on GET /api/auth/me, for the web banner and (later)
// the iPhone: plain text, shown from StartsAt until Until, dismissed per device by ID.
type siteNotice struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	StartsAt string `json:"starts_at"`
	Until    string `json:"until"`
	// LinkText / LinkURL: where the user can act on it (a same-site path).
	LinkText string `json:"link_text"`
	LinkURL  string `json:"link_url"`
}

// Active is the notice to show at now, or nil outside [From, Date).
func (t TransferNotice) Active(now time.Time) *siteNotice {
	if t.From.IsZero() || t.Date.IsZero() || now.Before(t.From) || !now.Before(t.Date) {
		return nil
	}
	day := t.Date.Format("January 2, 2006")
	return &siteNotice{
		ID: "transfer-" + t.Date.Format("2006-01-02"),
		// The DA-approved wording (llc-transfer-notice.md v3, "In-app notice").
		Text:     "From " + day + ", Rivolt will be run by ZeroDowntime LLC. Nothing changes for your data; details in the email we sent, or export or delete your data in Settings → Data.",
		StartsAt: t.From.UTC().Format(time.RFC3339),
		Until:    t.Date.UTC().Format(time.RFC3339),
		LinkText: "Settings → Data",
		LinkURL:  "/settings?tab=data",
	}
}
