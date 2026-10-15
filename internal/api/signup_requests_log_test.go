package api

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apohor/rivolt/internal/signuprequests"
)

// errDriver fails every query with an error that echoes the bound
// email, like a database unique-violation or a driver message would.
type errDriver struct{}

func (errDriver) Open(string) (driver.Conn, error) { return errConn{}, nil }

type errConn struct{}

func (errConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (errConn) Close() error                        { return nil }
func (errConn) Begin() (driver.Tx, error)           { return nil, errors.New("unused") }
func (errConn) QueryContext(_ context.Context, _ string, args []driver.NamedValue) (driver.Rows, error) {
	return nil, errors.New("insert failed for " + args[0].Value.(string))
}

func init() { sql.Register("signup-err-driver", errDriver{}) }

func TestSignupRequestCreateLogsNoEmail(t *testing.T) {
	d, err := sql.Open("signup-err-driver", "")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	h := handleSignupRequestCreate(signuprequests.New(d), nil, logger)

	req := httptest.NewRequest(http.MethodPost, "/api/signup/request",
		strings.NewReader(`{"email":"john.smith+qa@gmail.com"}`))
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	out := buf.String()
	if !strings.Contains(out, "signup request create failed") {
		t.Fatalf("expected warn line, got %q", out)
	}
	for _, bad := range []string{"@", "john", "smith"} {
		if strings.Contains(out, bad) {
			t.Errorf("log leaked %q: %s", bad, out)
		}
	}
}
