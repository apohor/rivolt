package api

import (
	"github.com/apohor/rivolt/internal/metrics"
)

// A failure to clear the identity provider's memory leaves a revoked browser able to sign straight back in, and the callers either
// carry on (plain sign-out always signs out of Rivolt) or report it to the person. Either way it must also be visible to us: a
// counter by entry point and step, no identity labels, so a Hydra or Kratos problem shows in Grafana before anyone notices a browser
// that would not stay signed out.

// forgetStepError tags an error with the step of the clearing that produced it.
type forgetStepError struct {
	step string
	err  error
}

func (e *forgetStepError) Error() string { return e.err.Error() }
func (e *forgetStepError) Unwrap() error { return e.err }

// forgetStep tags err (nil stays nil) with its step: account_lookup, identity_lookup, hydra_login, hydra_consent, kratos_sessions.
func forgetStep(step string, err error) error {
	if err == nil {
		return nil
	}
	return &forgetStepError{step: step, err: err}
}

// forgetSteps lists the steps that failed in err, which may be several errors joined; an error with no tag is "other".
func forgetSteps(err error) []string {
	seen := map[string]bool{}
	var out []string
	var walk func(error)
	walk = func(e error) {
		if e == nil {
			return
		}
		if t, ok := e.(*forgetStepError); ok {
			if !seen[t.step] {
				seen[t.step] = true
				out = append(out, t.step)
			}
			return
		}
		switch u := e.(type) {
		case interface{ Unwrap() []error }:
			for _, c := range u.Unwrap() {
				walk(c)
			}
		case interface{ Unwrap() error }:
			walk(u.Unwrap())
		default:
			if !seen["other"] {
				seen["other"] = true
				out = append(out, "other")
			}
		}
	}
	walk(err)
	if len(out) == 0 {
		out = []string{"other"}
	}
	return out
}

// ForgetObserver records the outcome of one clearing for an entry point. nil is a no-op.
type ForgetObserver func(entry string, err error)

// NewForgetObserver builds the observer over the metrics; nil metrics give a nil (no-op) observer.
func NewForgetObserver(m *metrics.Metrics) ForgetObserver {
	if m == nil {
		return nil
	}
	return func(entry string, err error) {
		m.LogoutForgetAttemptsTotal.WithLabelValues(entry).Inc()
		if err == nil {
			return
		}
		for _, step := range forgetSteps(err) {
			m.LogoutForgetFailuresTotal.WithLabelValues(entry, step).Inc()
		}
	}
}

func (o ForgetObserver) observe(entry string, err error) {
	if o != nil {
		o(entry, err)
	}
}
