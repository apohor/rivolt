package rivian

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"
)

// Rivian's OTA release information, as the owner app reads it: the version
// the vehicle runs and the one on offer, each with a release-notes link.
// (The live otaStatus / otaCurrentVersion / otaAvailableVersion fields of
// vehicleState are separate and already on State.)

const qOTAUpdateDetails = `query getOTAUpdateDetails($vehicleId: String!) {
  getVehicle(id: $vehicleId) {
    availableOTAUpdateDetails { url version locale }
    currentOTAUpdateDetails { url version locale }
  }
}`

// OTAVersion is one software release: its version string, the release-notes
// page Rivian publishes for it and the page's locale. Rivian's link is a
// pre-signed, one-hour URL (it carries an X-Amz-Security-Token), so it is a
// credential that has expired long before it is tapped: ReleaseNotesURL lives
// in memory only (json "-": never stored, exported or served), and HasNotes
// says a link existed. The app asks for a fresh one when the user taps
// (GET /api/vehicles/{id}/ota/notes). Rivolt never fetches the page itself.
type OTAVersion struct {
	Version         string `json:"version"`
	ReleaseNotesURL string `json:"-"`
	HasNotes        bool   `json:"has_notes,omitempty"`
	Locale          string `json:"locale,omitempty"`
}

// OTAInfo is what /api/vehicles reports under "ota".
type OTAInfo struct {
	Current   *OTAVersion `json:"current,omitempty"`
	Available *OTAVersion `json:"available,omitempty"`
	// Status is the live otaStatus from the vehicle state (idle,
	// downloading, ready_to_install, ...), filled when served, never stored.
	Status string `json:"status,omitempty"`
	// InstallProgress is the install's percent done, 0 to 100 (prod's otaInstallProgress is on that scale), present only while Status is
	// "installing" and never stored. It says nothing about how an install ends or what any other status word means.
	InstallProgress *int `json:"install_progress,omitempty"`
	// FetchedAt is when Rivian was last asked.
	FetchedAt time.Time `json:"fetched_at"`
}

// OTASource is what the OTA sync needs from a Rivian account; *LiveClient
// implements it.
type OTASource interface {
	OTAUpdateDetails(ctx context.Context, vehicleID string) (*OTAInfo, error)
}

type otaDetailsData struct {
	GetVehicle *struct {
		Available *otaDetail `json:"availableOTAUpdateDetails"`
		Current   *otaDetail `json:"currentOTAUpdateDetails"`
	} `json:"getVehicle"`
}

type otaDetail struct {
	URL     *string `json:"url"`
	Version *string `json:"version"`
	Locale  *string `json:"locale"`
}

// OTAUpdateDetails returns the current and available OTA releases for a
// vehicle. A release with no version is dropped; a release-notes URL that
// is not a plain https link is dropped (it ends up behind an <a>).
func (c *LiveClient) OTAUpdateDetails(ctx context.Context, vehicleID string) (*OTAInfo, error) {
	if strings.TrimSpace(vehicleID) == "" {
		return nil, errors.New("rivian: vehicleID is required")
	}
	headers, err := c.sessionHeaders()
	if err != nil {
		return nil, err
	}
	data, err := doGraphQLDetached[otaDetailsData](ctx, c, graphQLRequest{
		OperationName: "getOTAUpdateDetails",
		Query:         qOTAUpdateDetails,
		Variables:     map[string]any{"vehicleId": vehicleID},
	}, headers)
	if err != nil {
		return nil, fmt.Errorf("getOTAUpdateDetails: %w", err)
	}
	out := &OTAInfo{FetchedAt: time.Now().UTC()}
	if data.GetVehicle != nil {
		out.Current = toOTAVersion(data.GetVehicle.Current)
		out.Available = toOTAVersion(data.GetVehicle.Available)
	}
	return out, nil
}

func toOTAVersion(d *otaDetail) *OTAVersion {
	if d == nil || d.Version == nil || strings.TrimSpace(*d.Version) == "" {
		return nil
	}
	v := &OTAVersion{Version: strings.TrimSpace(*d.Version)}
	if d.URL != nil {
		v.ReleaseNotesURL = safeHTTPSURL(*d.URL)
		v.HasNotes = v.ReleaseNotesURL != ""
	}
	if d.Locale != nil {
		v.Locale = strings.TrimSpace(*d.Locale)
	}
	return v
}

// safeHTTPSURL returns raw when it is an absolute https URL with a host and
// no credentials, at most 2048 bytes; otherwise "".
func safeHTTPSURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2048 {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return ""
	}
	return raw
}

// OTAInstallPercent is the install progress to show: the vehicle's otaInstallProgress as a whole percent clamped to 0..100, and only while
// otaStatus is "installing" (nil for any other word, including ones we do not know, and for a value that is not a number).
func OTAInstallPercent(status string, progress float64) *int {
	if otaStatusClass(status) != "installing" || math.IsNaN(progress) || math.IsInf(progress, 0) {
		return nil
	}
	p := int(math.Round(math.Min(math.Max(progress, 0), 100)))
	return &p
}
