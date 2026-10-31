//go:build integration

package api

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/engage"
	"github.com/apohor/rivolt/internal/sessions"
)

// Scenario 10 of the N-instance harness, for the home-change notice: the iPhone app changes the home and the requests land on three
// pods in the same hour, each with its own in-memory limit and its own database pool; the account gets one email, and the next hour's
// change is a new notice.
func TestHomeChangeNoticeIsSentOnceAcrossPods(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	dsn := crossTenantDSN(ctx, t)
	mail := &recordingMailer{}
	now := time.Date(2026, 11, 20, 12, 5, 0, 0, time.UTC)
	look := func(context.Context, uuid.UUID) (string, error) { return "me@example.test", nil }
	var pods []*homeChangeNotice
	var pool0 *sql.DB
	for i := 0; i < 3; i++ {
		p, err := db.Open(ctx, dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = p.Close() })
		if i == 0 {
			pool0 = p
		}
		pods = append(pods, &homeChangeNotice{mail: mail, claims: engage.NewStore(p), now: func() time.Time { return now }, lookup: look})
	}
	uid, err := db.EnsureUser(ctx, pool0, "scn10-home")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, n := range pods {
		wg.Add(1)
		go func() { defer wg.Done(); n.afterHomeChange(homeRequest(sessions.KindApp, uid)) }()
	}
	wg.Wait()
	if len(mail.sent) != 1 {
		t.Fatalf("three pods sent %d home-change emails in one hour, want 1", len(mail.sent))
	}
	later := &homeChangeNotice{mail: mail, claims: engage.NewStore(pool0), now: func() time.Time { return now.Add(65 * time.Minute) }, lookup: look}
	later.afterHomeChange(homeRequest(sessions.KindApp, uid))
	if len(mail.sent) != 2 {
		t.Errorf("an hour later %d emails in all, want 2", len(mail.sent))
	}
}
