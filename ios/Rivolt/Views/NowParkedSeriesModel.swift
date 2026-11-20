import Foundation
import Observation

/// The last 24 hours of the parked truck's battery for the Now card (design_preview key now_parked_graph): stored rows only (no Rivian call),
/// read again every minute by the caller while the screen is up; nothing is kept past the screen. A failed read keeps what was there; an older
/// server without the endpoint (or a series with no line to draw) leaves the card as it was.
@MainActor
@Observable
final class NowParkedSeriesModel {
    static let refresh: TimeInterval = 60

    /// How long the chip waits for the first answer before it shows the state's own parked time.
    static let chipWait: Duration = .seconds(3)

    private(set) var series: ParkedSeries?
    /// True once a read has answered or failed: until then the chip shows no parked time, so it never changes by hours as the series arrives.
    private(set) var settled = false
    /// The truck the series (and the wait) belongs to: another truck starts over.
    private(set) var vehicleID: String?

    func load(source: some ParkedSeriesSource, vehicleID: String, zone: String = TimeZone.current.identifier) async {
        if self.vehicleID != vehicleID { clear() }
        self.vehicleID = vehicleID
        do {
            apply(try await source.parkedSeries(vehicleID: vehicleID, tz: zone))
            settled = true
        } catch {
            if Task.isCancelled { return }
            settled = true
        }
    }

    func apply(_ s: ParkedSeries?) { series = NowParkedGraphModel.drawable(s) ? s : nil }

    func clear() { series = nil; settled = false; vehicleID = nil }
}
