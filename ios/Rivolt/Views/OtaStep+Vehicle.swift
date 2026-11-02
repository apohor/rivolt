import Foundation

extension OtaStep {
    /// From a vehicle's `ota` object on `GET /api/vehicles` (the app only; the widget has no vehicle list).
    init?(_ ota: Vehicle.OTA?) {
        guard let ota else { return nil }
        self.init(step: ota.step, progress: ota.installProgress, version: ota.available?.version)
    }
}
