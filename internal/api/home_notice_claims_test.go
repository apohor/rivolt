package api

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/engage"
	"github.com/apohor/rivolt/internal/sessions"
)

// memClaims is the engagement claim table in memory: the first claim of (user, kind, key) wins, as the INSERT ... ON CONFLICT DO NOTHING does.
type memClaims struct {
	engage.Store
	mu     sync.Mutex
	rows   map[string]string
	claims []engage.Msg
}

func (c *memClaims) Claim(_ context.Context, m engage.Msg, _, _ time.Time, _ int) (bool, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := m.User.String() + "/" + m.Kind + "/" + m.Key
	if _, ok := c.rows[k]; ok {
		return false, false, nil
	}
	if c.rows == nil {
		c.rows = map[string]string{}
	}
	c.rows[k] = "sending"
	c.claims = append(c.claims, m)
	return true, false, nil
}

func (c *memClaims) Mark(_ context.Context, uid uuid.UUID, kind, key, status string, _ time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rows[uid.String()+"/"+kind+"/"+key] = status
	return nil
}

// Two pods that both see an app home change in the same hour send one email between them, as a utility claim keyed by the hour; the next
// hour is a new notice.
func TestHomeChangeNoticeIsClaimedAcrossPods(t *testing.T) {
	mail := &recordingMailer{}
	claims := &memClaims{}
	now := time.Date(2026, 10, 25, 12, 5, 0, 0, time.UTC)
	look := func(context.Context, uuid.UUID) (string, error) { return "me@example.com", nil }
	podA := &homeChangeNotice{mail: mail, claims: claims, now: func() time.Time { return now }, lookup: look}
	podB := &homeChangeNotice{mail: mail, claims: claims, now: func() time.Time { return now.Add(10 * time.Minute) }, lookup: look}
	uid := uuid.New()

	podA.afterHomeChange(homeRequest(sessions.KindApp, uid))
	podB.afterHomeChange(homeRequest(sessions.KindApp, uid))
	if len(mail.sent) != 1 {
		t.Fatalf("two pods sent %d emails, want 1", len(mail.sent))
	}
	if len(claims.claims) != 1 || claims.claims[0].Kind != homeNoticeKind || !claims.claims[0].Utility || claims.claims[0].Channel != engage.Email || claims.claims[0].Key != "2026102512" {
		t.Errorf("claims = %+v, want one utility email claim keyed by the hour", claims.claims)
	}
	if got := claims.rows[uid.String()+"/"+homeNoticeKind+"/2026102512"]; got != "sent" {
		t.Errorf("claim status %q, want sent", got)
	}
	later := &homeChangeNotice{mail: mail, claims: claims, now: func() time.Time { return now.Add(70 * time.Minute) }, lookup: look}
	later.afterHomeChange(homeRequest(sessions.KindApp, uid))
	if len(mail.sent) != 2 {
		t.Errorf("an hour later %d emails in all, want 2", len(mail.sent))
	}
}
