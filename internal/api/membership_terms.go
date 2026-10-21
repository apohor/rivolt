package api

import (
	"net/http"
	"time"

	"github.com/apohor/rivolt/internal/dcfcrates"
)

// membershipTermJSON is one plan's terms as the operator states them, with its
// source and the date it was last checked.
type membershipTermJSON struct {
	NetworkSlug string `json:"network_slug"`
	Network     string `json:"network"`
	Plan        string `json:"plan"`
	Terms       string `json:"terms"`
	SourceURL   string `json:"source_url"`
	ReadVia     string `json:"read_via"`
	Checked     string `json:"checked"`
	Label       string `json:"label"`
}

// handleMembershipTerms serves GET /api/settings/charging/membership-terms: the
// networks' membership terms from sourced facts only. Public reference data, the
// same for everyone (nothing about the caller is read or stored). Rows last checked
// over 12 months ago are not returned; every row carries its label ("As listed by
// <network>; may be out of date; last checked <date>.").
func handleMembershipTerms(now func() time.Time) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows := dcfcrates.MembershipTermsAt(now())
		out := make([]membershipTermJSON, 0, len(rows))
		for _, t := range rows {
			out = append(out, membershipTermJSON{
				NetworkSlug: t.Slug, Network: t.Network, Plan: t.Plan, Terms: t.Terms,
				SourceURL: t.SourceURL, ReadVia: t.ReadVia, Checked: t.Checked, Label: t.Label,
			})
		}
		w.Header().Set("Cache-Control", "private, max-age=3600")
		writeJSON(w, http.StatusOK, map[string]any{"terms": out})
	}
}
