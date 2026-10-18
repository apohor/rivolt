//go:build integration

// Package testpg gives the integration tests a Postgres without paying for a
// container each: one container per test binary, migrated once into a
// template database, and every test gets its own database cloned from it
// (milliseconds, not the seconds a container start plus 60 migrations cost).
// A test still sees an empty, fully migrated database of its own, dropped when
// the test ends.
package testpg

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/apohor/rivolt/internal/db"
)

const (
	adminDB  = "rivolt_test"
	template = "rivolt_tmpl"
)

var (
	once     sync.Once
	hostPort string
	startErr error
	createMu sync.Mutex // CREATE DATABASE ... TEMPLATE wants the template unshared
)

func dsnFor(database string) string {
	return fmt.Sprintf("postgres://rivolt:rivolt@%s/%s?sslmode=disable", hostPort, database)
}

// start runs the container and builds the migrated template. The container is
// left to testcontainers' reaper when the test binary exits.
func start() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "postgres:16-alpine",
			ExposedPorts: []string{"5432/tcp"},
			Env:          map[string]string{"POSTGRES_USER": "rivolt", "POSTGRES_PASSWORD": "rivolt", "POSTGRES_DB": adminDB},
			WaitingFor: wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		return fmt.Errorf("start postgres: %w", err)
	}
	host, err := c.Host(ctx)
	if err != nil {
		return err
	}
	port, err := c.MappedPort(ctx, "5432/tcp")
	if err != nil {
		return err
	}
	hostPort = fmt.Sprintf("%s:%s", host, port.Port())

	admin, err := sql.Open("pgx", dsnFor(adminDB))
	if err != nil {
		return err
	}
	defer admin.Close()
	if _, err := admin.ExecContext(ctx, `CREATE DATABASE `+template); err != nil {
		return fmt.Errorf("create template: %w", err)
	}
	m, err := db.Open(ctx, dsnFor(template)) // applies every migration
	if err != nil {
		return fmt.Errorf("migrate template: %w", err)
	}
	return m.Close() // no open sessions on the template, or it cannot be cloned
}

func skipIfOff(t *testing.T) {
	t.Helper()
	if os.Getenv("RIVOLT_SKIP_INTEGRATION") != "" {
		t.Skip("RIVOLT_SKIP_INTEGRATION set")
	}
}

func begin(t *testing.T) {
	t.Helper()
	skipIfOff(t)
	once.Do(func() { startErr = start() })
	if startErr != nil {
		t.Fatalf("postgres for tests: %v", startErr)
	}
}

// create makes a database (cloned from tmpl when it is not empty) and
// arranges for it to be dropped after the test.
func create(ctx context.Context, t *testing.T, tmpl string) string {
	t.Helper()
	var b [6]byte
	_, _ = rand.Read(b[:])
	name := "t_" + hex.EncodeToString(b[:])
	admin, err := sql.Open("pgx", dsnFor(adminDB))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	q := `CREATE DATABASE ` + name
	if tmpl != "" {
		q += ` TEMPLATE ` + tmpl
	}
	createMu.Lock()
	_, err = admin.ExecContext(ctx, q)
	createMu.Unlock()
	if err != nil {
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		a, err := sql.Open("pgx", dsnFor(adminDB))
		if err != nil {
			return
		}
		defer a.Close()
		_, _ = a.ExecContext(c, `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`)
	})
	return dsnFor(name)
}

// DSN returns a connection string for a new, fully migrated, empty database.
func DSN(ctx context.Context, t *testing.T) string {
	t.Helper()
	begin(t)
	return create(ctx, t, template)
}

// Fresh returns a connection string for a new database with NO migrations
// applied (for tests of the migration and repair paths themselves).
func Fresh(ctx context.Context, t *testing.T) string {
	t.Helper()
	begin(t)
	return create(ctx, t, "")
}

// flake-guard experiment marker (not for merge)
