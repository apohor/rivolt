package rivian

import (
	"regexp"
	"strings"
)

// Observing what vehicleState says about a software update, to learn what we cannot read from the code: the words
// otaStatus takes (only "Idle" has ever been seen in a captured frame) and the scale of otaInstallProgress (0 to 100 or
// 0 to 1). Nothing is stored. It counts each status as a bounded class (rivolt_ota_status_frames_total), and logs the
// first time each distinct status word is seen by this process and, per vehicle, the first non-zero progress and the
// first one above 1 (which settles the scale). A log carries the status word and the number only, plus the vehicle id
// every other log line here already carries.

// OTAObserver is optionally implemented by the SleepObserver (main's metrics adapter): told the class of each
// vehicleState frame's otaStatus.
type OTAObserver interface {
	OnOTAStatus(class string)
}

// otaKnownStatuses are the words we expect (lower case, spaces and dashes as underscores). Anything else is counted
// as "other" and logged once, so the counter's labels stay bounded whatever Rivian sends.
var otaKnownStatuses = map[string]bool{
	"idle": true, "downloading": true, "download_complete": true, "ready_to_install": true, "installing": true,
	"installed": true, "scheduled": true, "waiting": true, "paused": true, "failed": true, "cancelled": true,
	// seen on prod trucks and in Rivian's app (Nov 2): the counter's "other" bucket held these
	"preparing": true, "ready_to_download": true, "scheduled_to_install": true, "install_countdown": true, "awaiting_install": true,
	"install_success": true, "connection_lost": true,
}

// otaWordOK limits what is logged as a status word: a short plain word, never free text.
var otaWordOK = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_ -]{0,31}$`)

// otaMaxWords bounds the set of distinct words remembered (and so logged) per process.
const otaMaxWords = 32

// otaStatusKey is a status word in lower case with spaces and dashes as underscores ("Install_Success" -> "install_success").
func otaStatusKey(s string) string {
	k := strings.ToLower(strings.TrimSpace(s))
	return strings.NewReplacer(" ", "_", "-", "_").Replace(k)
}

// otaStatusClass normalises a status word to a counter label: the known word, or "other".
func otaStatusClass(s string) string {
	k := otaStatusKey(s)
	if otaKnownStatuses[k] {
		return k
	}
	return "other"
}

// observeOTA reports one vehicleState push's OTA fields. Empty status and zero progress are frames that carry no
// OTA information (vehicleState pushes only what changed) and are ignored.
func (m *StateMonitor) observeOTA(vehicleID string, st *State) {
	if m == nil || st == nil {
		return
	}
	status := strings.TrimSpace(st.OtaStatus)
	progress := st.OtaInstallProgress
	if status == "" && progress == 0 {
		return
	}
	var logStatus, logProgress, logScale bool
	m.mu.Lock()
	obs := m.sleepObserver
	if status != "" && len(m.otaWords) < otaMaxWords && !m.otaWords[strings.ToLower(status)] {
		if m.otaWords == nil {
			m.otaWords = map[string]bool{}
		}
		m.otaWords[strings.ToLower(status)] = true
		logStatus = true
	}
	if progress != 0 && !m.otaProgress[vehicleID] {
		if m.otaProgress == nil {
			m.otaProgress = map[string]bool{}
		}
		m.otaProgress[vehicleID] = true
		logProgress = true
	}
	if progress > 1 && !m.otaScale[vehicleID] {
		if m.otaScale == nil {
			m.otaScale = map[string]bool{}
		}
		m.otaScale[vehicleID] = true
		logScale = true
	}
	m.mu.Unlock()
	if o, ok := obs.(OTAObserver); ok && status != "" {
		o.OnOTAStatus(otaStatusClass(status))
	}
	if m.logger == nil {
		return
	}
	word := status
	if !otaWordOK.MatchString(word) {
		word = "(not a plain word)"
	}
	if logStatus {
		m.logger.Info("ota status seen", "vehicle", vehicleID, "status", word, "class", otaStatusClass(status))
	}
	if logProgress {
		m.logger.Info("ota install progress seen", "vehicle", vehicleID, "progress", progress, "status", word)
	}
	if logScale {
		m.logger.Info("ota install progress above 1 (so a 0 to 100 scale)", "vehicle", vehicleID, "progress", progress, "status", word)
	}
}
