package rivian

import (
	"context"
	"errors"
	"fmt"
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
// page Rivian publishes for it and the page's locale. ReleaseNotesURL is an
// outbound link for the UI to show; Rivolt never fetches it.
type OTAVersion struct {
	Version         string `json:"version"`
	ReleaseNotesURL string `json:"release_notes_url,omitempty"`
	Locale          string `json:"locale,omitempty"`
}

// OTAInfo is what /api/vehicles reports under "ota".
type OTAInfo struct {
	Current   *OTAVersion `json:"current,omitempty"`
	Available *OTAVersion `json:"available,omitempty"`
	// Status is the live otaStatus from the vehicle state (idle,
	// downloading, ready_to_install, ...), filled when served, never stored.
	Status string `json:"status,omitempty"`
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
