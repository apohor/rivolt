package api

import (
	"database/sql"
	"testing"

	"github.com/apohor/rivolt/internal/settings"
)

// Code that takes a Deps without going through New (the handlers the integration tests call directly, the sweeps) must find the
// system pool and the system-side stores even when only the request-side ones are set, which is every deployment without
// RIVOLT_REQUEST_DATABASE_URL: SystemDB, SystemSettings and SystemSecrets default to their request-side twins.
func TestSystemSideAccessorsFallBackToTheRequestSide(t *testing.T) {
	db1, db2 := new(sql.DB), new(sql.DB)
	set1, set2 := new(settings.Factory), new(settings.Factory)

	d := Deps{DB: db1, Settings: set1}
	if d.sysDB() != db1 || d.systemSettings() != set1 {
		t.Fatal("with no system side set, the accessors must return the request side")
	}
	d.SystemDB, d.SystemSettings = db2, set2
	if d.sysDB() != db2 || d.systemSettings() != set2 {
		t.Fatal("with the system side set, the accessors must return it")
	}
}
