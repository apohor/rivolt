package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/apohor/rivolt/internal/forward"
	"github.com/apohor/rivolt/internal/rivian"
)

// otaNotesClassName marks an owner reply that carries an otaFailure body.
const otaNotesClassName = "ota_error"

// otaNotesLinks is what the handler needs from a Rivian read of a vehicle's release details: the two links that passed the host check,
// either of which may be empty.
type otaNotesLinks struct {
	Current   string `json:"current"`
	Available string `json:"available"`
}

// otaFailure is a failed read on the wire: a kind and nothing else. The error's own text is never sent: it can carry the signed link.
type otaFailure struct {
	Kind string `json:"kind"` // paused | needs_reauth | not_authenticated | other
}

var (
	errOTAFailed = errors.New("release notes read failed")
)

// otaNotesLinksOf reads the release details from src and keeps the two links the host check passes. It is the one Rivian call of the
// release-notes handler, run on the pod that holds the vehicle's group (or here, when the forward falls soft).
func otaNotesLinksOf(ctx context.Context, src rivian.OTASource, vehicleID string) (otaNotesLinks, error) {
	info, err := src.OTAUpdateDetails(ctx, vehicleID)
	if err != nil {
		return otaNotesLinks{}, err
	}
	var l otaNotesLinks
	if info != nil && info.Current != nil && otaNotesHostOK(info.Current.ReleaseNotesURL) {
		l.Current = info.Current.ReleaseNotesURL
	}
	if info != nil && info.Available != nil && otaNotesHostOK(info.Available.ReleaseNotesURL) {
		l.Available = info.Available.ReleaseNotesURL
	}
	return l, nil
}

// forwardOTANotes reads the links through the vehicle's group owner when this pod does not hold the group. Only the live client reaches
// Rivian, so anything else (and a nil forwarder, an unknown group, a soft failure) runs the read here exactly as before.
func (sf *stateForward) forwardOTANotes(ctx context.Context, src rivian.OTASource, vehicleID string) (otaNotesLinks, error) {
	local := func(ctx context.Context) (otaNotesLinks, error) { return otaNotesLinksOf(ctx, src, vehicleID) }
	if sf == nil || sf.fw == nil || sf.groupOf == nil {
		return local(ctx)
	}
	if !sf.forwardable(src) {
		return local(ctx)
	}
	group, ok := sf.groupOf(vehicleID)
	if !ok {
		sf.fw.NoGroup(forward.KindOTANotes)
		return local(ctx)
	}
	var (
		links otaNotesLinks
		lerr  error
		ran   bool
	)
	res, _, err := sf.fw.Do(ctx, forward.Call{Kind: forward.KindOTANotes, User: sf.user.String(), Group: group, Args: forward.OTANotesArgs{VehicleID: vehicleID}},
		func(ctx context.Context) (forward.Result, error) {
			ran = true
			links, lerr = local(ctx)
			return forward.Result{Status: http.StatusOK}, nil
		})
	if ran {
		return links, lerr
	}
	if err != nil {
		return otaNotesLinks{}, err
	}
	return decodeForwardedOTANotes(res)
}

func decodeForwardedOTANotes(res forward.Result) (otaNotesLinks, error) {
	if res.Status == http.StatusOK {
		var l otaNotesLinks
		if err := json.Unmarshal(res.Body, &l); err != nil || !otaLinkOK(l.Current) || !otaLinkOK(l.Available) {
			return otaNotesLinks{}, errOTAFailed // the requester checks the host again: a reply never makes it an open redirect
		}
		return l, nil
	}
	if res.Class == otaNotesClassName {
		var f otaFailure
		if json.Unmarshal(res.Body, &f) == nil {
			switch f.Kind {
			case "paused":
				return otaNotesLinks{}, rivian.ErrUpstreamPaused
			case "needs_reauth":
				return otaNotesLinks{}, rivian.ErrNeedsReauth
			case "not_authenticated":
				return otaNotesLinks{}, rivian.ErrNotAuthenticated
			}
		}
		return otaNotesLinks{}, errOTAFailed
	}
	return otaNotesLinks{}, errOwnerUnavailable
}

func otaLinkOK(l string) bool { return l == "" || otaNotesHostOK(l) }

// ServeOTANotes is the owner's side of a forwarded ota_notes: the same read as the handler's, answered as the two checked links, or as a
// failure kind with no text.
func ServeOTANotes(ctx context.Context, c rivian.Client, vehicleID string) forward.Result {
	var src rivian.OTASource
	if c != nil {
		src, _ = c.(rivian.OTASource)
	}
	if src == nil {
		raw, _ := json.Marshal(otaNotesLinks{})
		return forward.Result{Status: http.StatusOK, Body: raw}
	}
	links, err := otaNotesLinksOf(ctx, src, vehicleID)
	if err != nil {
		f := otaFailure{Kind: "other"}
		switch {
		case errors.Is(err, rivian.ErrUpstreamPaused):
			f.Kind = "paused"
		case errors.Is(err, rivian.ErrNeedsReauth):
			f.Kind = "needs_reauth"
		case errors.Is(err, rivian.ErrNotAuthenticated):
			f.Kind = "not_authenticated"
		}
		raw, _ := json.Marshal(f)
		return forward.Result{Status: http.StatusBadGateway, Class: otaNotesClassName, Body: raw}
	}
	raw, _ := json.Marshal(links)
	return forward.Result{Status: http.StatusOK, Body: raw}
}
