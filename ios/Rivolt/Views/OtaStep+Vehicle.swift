import Foundation

extension OtaStep {
    /// From the live state (`GET /api/state`): the step and percent the server derives, and the pending version (the app only; the widget has none).
    init?(_ state: VehicleState?) {
        guard let state else { return nil }
        self.init(step: state.otaStep, progress: state.otaInstallPercent, version: state.otaAvailableVersion)
    }
}
