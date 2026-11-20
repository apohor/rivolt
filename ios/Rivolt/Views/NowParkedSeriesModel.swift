import Foundation
import Observation

/// The last 24 hours of the parked truck's battery for the Now card (design_preview key now_parked_graph): stored rows only (no Rivian call),
/// read again every minute by the caller while the screen is up; nothing is kept past the screen. A failed read keeps what was there; an older
/// server without the endpoint (or a series with no line to draw) leaves the card as it was.
@MainActor
@Observable
final class NowParkedSeriesModel {
    static let refresh: TimeInterval = 60

    private(set) var series: ParkedSeries?

    func load(source: some ParkedSeriesSource, vehicleID: String, zone: String = TimeZone.current.identifier) async {
        do {
            apply(try await source.parkedSeries(vehicleID: vehicleID, tz: zone))
        } catch {
            if Task.isCancelled { return }
        }
    }

    func apply(_ s: ParkedSeries?) { series = NowParkedGraphModel.drawable(s) ? s : nil }

    func clear() { series = nil }
}
