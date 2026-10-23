package rivian

import "regexp"

// Observing what vehicleState says about each tire's status, to learn what we cannot read from the code: which words
// prod trucks send, and whether any ever says low. Nothing is stored. Each wheel on a frame that carries tire data is
// counted in a bounded class (rivolt_tire_status_total: low, not_low, other, missing: no vehicle, user or raw word as a
// label), and the first time this monitor (one per user) sees a word it has no claim for, it is logged once, with the user_id every
// log line of that monitor carries and no vehicle id. The
// words are Rivian enum strings, not personal data.

// TireObserver is optionally implemented by the SleepObserver (main's metrics adapter): told the class of each wheel's
// status on a frame that carries tire data.
type TireObserver interface {
	OnTireStatus(class string)
}

// tireWordOK limits what is logged as a word: a short plain word, never free text.
var tireWordOK = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_ -]{0,31}$`)

// tireMaxWords bounds the set of distinct "other" words remembered (and so logged) per monitor.
const tireMaxWords = 32

// observeTires reports one vehicleState push's tire fields. A frame with no tire pressure and no tire status carries no
// tire information (vehicleState pushes only what changed) and is ignored; on one that does, a wheel with a pressure
// but no status counts as missing.
func (m *StateMonitor) observeTires(st *State) {
	if m == nil || st == nil {
		return
	}
	wheels := [4]struct {
		bar    float64
		status string
	}{
		{st.TirePressureFLBar, st.TirePressureStatusFL}, {st.TirePressureFRBar, st.TirePressureStatusFR},
		{st.TirePressureRLBar, st.TirePressureStatusRL}, {st.TirePressureRRBar, st.TirePressureStatusRR},
	}
	any := false
	for _, w := range wheels {
		if w.bar != 0 || tireWord(w.status) != "" {
			any = true
		}
	}
	if !any {
		return
	}
	var newWords []string
	m.mu.Lock()
	obs := m.sleepObserver
	for _, w := range wheels {
		if tireStatusClass(w.status) != tireClassOther {
			continue
		}
		k := tireWord(w.status)
		if len(m.tireWords) < tireMaxWords && !m.tireWords[k] {
			if m.tireWords == nil {
				m.tireWords = map[string]bool{}
			}
			m.tireWords[k] = true
			if tireWordOK.MatchString(w.status) {
				newWords = append(newWords, w.status)
			} else {
				newWords = append(newWords, "(not a plain word)")
			}
		}
	}
	m.mu.Unlock()
	if o, ok := obs.(TireObserver); ok {
		for _, w := range wheels {
			if w.bar == 0 && tireWord(w.status) == "" {
				continue // this wheel says nothing on this frame
			}
			o.OnTireStatus(tireStatusClass(w.status))
		}
	}
	if m.logger != nil {
		for _, w := range newWords {
			m.logger.Info("tire status word seen", "status", w)
		}
	}
}
