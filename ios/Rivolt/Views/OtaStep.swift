import Foundation

/// The truck's software update step as the server reports it (`ota.step` and `ota.install_progress` on `GET /api/vehicles`), the web's `otaStep`.
/// Only the ten steps the truck is known to send are named, in plain words; `unknown`, any other word and no step at all show nothing (a failed
/// update arrives as `unknown`, so "Updating…" would falsely say it is in progress). A number only while installing; "Installed" only for
/// `install_success`, never inferred from 100% or from the step disappearing.
struct OtaStep: Equatable {
    let word: String
    let pct: Int?
    let version: String?

    private static let words: [String: String] = [
        "ready_to_download": "Ready to download", "downloading": "Downloading", "preparing": "Preparing", "ready_to_install": "Ready to install",
        "scheduled_to_install": "Install scheduled", "install_countdown": "Install about to start", "awaiting_install": "Waiting to install",
        "installing": "Installing", "install_success": "Installed", "connection_lost": "Connection lost",
    ]

    init?(_ ota: Vehicle.OTA?) {
        guard let ota, let step = ota.step, let word = Self.words[step] else { return nil }
        self.word = word
        if step == "installing", let p = ota.installProgress { pct = min(100, max(0, p)) } else { pct = nil }
        let v = (ota.available?.version ?? "").trimmingCharacters(in: .whitespaces)
        version = v.isEmpty || v.allSatisfy({ $0 == "0" || $0 == "." }) ? nil : v
    }

    /// "2026.38 · Installing 37%", "Ready to install".
    var line: String {
        let w = pct.map { "\(word) \($0)%" } ?? word
        return version.map { "\($0) · \(w)" } ?? w
    }
}
