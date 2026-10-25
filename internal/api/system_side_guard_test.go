package api

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/apohor/rivolt/internal/secrets"
	"github.com/apohor/rivolt/internal/settings"
)

// With the request pool on, a missing system-side store would silently run the system sites on the request pool (account deletion's
// last-admin count would see only the caller). api.New refuses to build instead.
func TestNewRefusesARequestPoolWithoutItsSystemSide(t *testing.T) {
	req, sys := new(sql.DB), new(sql.DB)
	set, sset := new(settings.Factory), new(settings.Factory)
	sec, ssec := new(secrets.Store), new(secrets.Store)

	full := Deps{RequestPoolOn: true, DB: req, SystemDB: sys, Settings: set, SystemSettings: sset, Secrets: sec, SystemSecrets: ssec}
	if err := full.checkSystemSide(); err != nil {
		t.Fatalf("a complete system side was refused: %v", err)
	}

	for name, mutate := range map[string]func(*Deps){
		"SystemDB":       func(d *Deps) { d.SystemDB = nil },
		"SystemSettings": func(d *Deps) { d.SystemSettings = nil },
		"SystemSecrets":  func(d *Deps) { d.SystemSecrets = nil },
	} {
		d := full
		mutate(&d)
		err := d.checkSystemSide()
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s missing: err = %v, want a refusal naming it", name, err)
		}
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s missing: New built a router instead of refusing", name)
				}
			}()
			New(d)
		}()
	}

	// Present but the same object as the request side: one pool under two names.
	for name, mutate := range map[string]func(*Deps){
		"SystemDB is DB":             func(d *Deps) { d.SystemDB = d.DB },
		"SystemSettings is Settings": func(d *Deps) { d.SystemSettings = d.Settings },
		"SystemSecrets is Secrets":   func(d *Deps) { d.SystemSecrets = d.Secrets },
	} {
		d := full
		mutate(&d)
		if err := d.checkSystemSide(); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: err = %v, want a refusal naming it", name, err)
		}
	}

	// Pool off: nothing is required, and the fallbacks apply (every deployment today).
	off := Deps{DB: req}
	if err := off.checkSystemSide(); err != nil {
		t.Fatalf("pool off: %v", err)
	}
	// A twin that is not set on the request side needs no system side (no sealer, or the stub client).
	noSecrets := Deps{RequestPoolOn: true, DB: req, SystemDB: sys}
	if err := noSecrets.checkSystemSide(); err != nil {
		t.Fatalf("no Settings or Secrets configured: %v", err)
	}
}
