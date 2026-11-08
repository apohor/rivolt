package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/forward"
	"github.com/apohor/rivolt/internal/rivian"
)

// StateForwarder is the forwarder as the vehicle-state handler uses it (*forward.Forwarder satisfies it).
type StateForwarder interface {
	// NoGroup counts a call served on this pod because the vehicle's group is unknown.
	NoGroup(kind forward.Kind)
	Do(ctx context.Context, c forward.Call, local func(context.Context) (forward.Result, error)) (forward.Result, string, error)
}

// freshStateMaxAge is how fresh the owner's own cache has to be for it to answer a forwarded fetch without a REST call: the owner's
// vehicleState subscription keeps it current, so a recent entry is as good as a fetch and costs Rivian nothing.
const freshStateMaxAge = 20 * time.Second

// stateForward routes the `fetch` branch of GET /api/state/{id} (the only place that handler calls Rivian) through the forwarder: on a
// pod that does not hold the vehicle's group, the group's owner makes the call. A nil *stateForward, or a vehicle whose group this pod does
// not know, keeps today's behaviour: the pod calls Rivian itself.
type stateForward struct {
	fw      StateForwarder
	groupOf func(vehicleID string) (string, bool)
	user    uuid.UUID
}

// stateForward is the per-request router for uid, nil when forwarding is not wired.
func (d Deps) stateForward(uid uuid.UUID) *stateForward {
	if d.StateForward == nil || d.GroupOfVehicle == nil {
		return nil
	}
	return &stateForward{fw: d.StateForward, groupOf: d.GroupOfVehicle, user: uid}
}

// fetch is c.State for vehicleID, forwarded when this pod does not own the vehicle.
func (sf *stateForward) fetch(ctx context.Context, c rivian.Client, vehicleID string) (*rivian.State, error) {
	if sf == nil || sf.fw == nil || sf.groupOf == nil {
		return c.State(ctx, vehicleID)
	}
	group, ok := sf.groupOf(vehicleID)
	if !ok {
		sf.fw.NoGroup(forward.KindFreshState)
		return c.State(ctx, vehicleID)
	}
	var (
		st   *rivian.State
		lerr error
		ran  bool
	)
	call := forward.Call{Kind: forward.KindFreshState, User: sf.user.String(), Group: group,
		Args: forward.FreshStateArgs{VehicleID: vehicleID, MaxAgeMS: freshStateMaxAge.Milliseconds()}}
	res, _, err := sf.fw.Do(ctx, call, func(ctx context.Context) (forward.Result, error) {
		ran = true // this pod serves it (it holds the group, or the call fell soft): the exact code that ran before
		st, lerr = c.State(ctx, vehicleID)
		return forward.Result{Status: http.StatusOK}, nil
	})
	if ran {
		return st, lerr
	}
	if err != nil {
		return nil, err
	}
	return decodeForwardedState(res)
}

// forwardedStateError is a failed forwarded fetch: the status and JSON body the owner would have sent, written back unchanged.
type forwardedStateError struct {
	Status int
	Class  string
	Body   json.RawMessage
}

func (e *forwardedStateError) Error() string {
	return "forwarded state fetch failed: " + strconv.Itoa(e.Status)
}

// write sends the owner's answer as this handler would have: the same status and body, plus the headers the direct path sets.
func (e *forwardedStateError) write(w http.ResponseWriter) {
	switch {
	case e.Status == http.StatusTooManyRequests:
		w.Header().Set("Retry-After", strconv.Itoa(upstreamRetryAfterSec))
	case e.Class == forward.ClassUnavailable:
		w.Header().Set("Retry-After", strconv.Itoa(forward.RetryAfterSeconds))
	}
	if e.Class == classNotImplemented {
		var msg string
		_ = json.Unmarshal(e.Body, &msg)
		http.Error(w, msg, http.StatusNotFound)
		return
	}
	if len(e.Body) == 0 {
		writeJSON(w, e.Status, map[string]any{"error": "the vehicle's owner did not answer; try again", "class": forward.ClassUnavailable})
		return
	}
	writeJSON(w, e.Status, e.Body)
}

// classNotImplemented marks the owner's rivian.ErrNotImplemented, which the handler answers as a plain-text 404.
const classNotImplemented = "not_implemented"

func decodeForwardedState(res forward.Result) (*rivian.State, error) {
	if res.Status != http.StatusOK {
		return nil, &forwardedStateError{Status: res.Status, Class: res.Class, Body: res.Body}
	}
	var st rivian.State
	if err := json.Unmarshal(res.Body, &st); err != nil {
		return nil, &forwardedStateError{Status: http.StatusBadGateway, Class: forward.ClassBadReply}
	}
	return &st, nil
}

// ServeFreshState is the owner's side of a forwarded fresh_state: the same fetch the handler makes, answered as a forward.Result. If the
// owner's own cache is no older than maxAge it answers from it (the subscription keeps it current); otherwise it fetches from Rivian,
// primes its cache and answers with the state. A failure is answered with exactly the status and body the handler would have written, so
// the requester's client sees the same error whichever pod served it.
func ServeFreshState(ctx context.Context, c rivian.Client, mon *rivian.StateMonitor, hasSession func(context.Context) (bool, error), vehicleID string, maxAge time.Duration) forward.Result {
	if c == nil {
		return forward.Result{Status: http.StatusNotFound, Class: classNotImplemented, Body: marshalRaw("no rivian client configured")}
	}
	if mon != nil && maxAge > 0 {
		if st, at := mon.Latest(vehicleID); st != nil && !at.IsZero() && time.Since(at) <= maxAge {
			return stateResult(st)
		}
	}
	st, err := c.State(ctx, vehicleID)
	if err != nil {
		if errors.Is(err, rivian.ErrNotImplemented) {
			return forward.Result{Status: http.StatusNotFound, Class: classNotImplemented, Body: marshalRaw(err.Error())}
		}
		status, body := stateErrorResponse(ctx, err, hasSession)
		raw, _ := json.Marshal(body)
		class := ""
		if m, ok := body.(map[string]any); ok {
			class, _ = m["class"].(string)
		}
		if nc, ok := body.(notConnectedBody); ok {
			class = nc.Class
		}
		return forward.Result{Status: status, Class: class, Body: raw}
	}
	if mon != nil {
		mon.Prime(vehicleID, st)
	}
	return stateResult(st)
}

func stateResult(st *rivian.State) forward.Result {
	raw, err := json.Marshal(st)
	if err != nil {
		return forward.Result{Status: http.StatusBadGateway, Class: forward.ClassInternal}
	}
	return forward.Result{Status: http.StatusOK, Body: raw}
}

func marshalRaw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
