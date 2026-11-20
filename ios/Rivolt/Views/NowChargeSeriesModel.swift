import Foundation
import Observation

/// The open charge's stored samples for the Now card (design_preview key now_charge_graph): the graph draws them and the temperatures line takes
/// the pack's reading from them. Stored rows only (`/api/samples`, no Rivian call), read again at the card's own 30 s pace by the caller while the
/// screen is up; nothing is kept past the screen.
@MainActor
@Observable
final class NowChargeSeriesModel {
    /// The Live page's pace; the server stores a sample about this often.
    static let refresh: TimeInterval = 30
    /// A pack reading older than this is not shown.
    static let packFresh: TimeInterval = 15 * 60

    private(set) var series = ChargeCurve.Series()
    /// The first stored reading of the open charge (the graph's start label), unsmoothed.
    private(set) var startPct: Double?
    private(set) var pack: (avgC: Double, at: Date)?

    /// Whose charge the rows are: the truck and the moment the charge began.
    private var owner: String?

    private static func owner(_ vehicleID: String?, _ startedAt: Date?) -> String? {
        guard let vehicleID, let startedAt else { return nil }
        return "\(vehicleID)|\(startedAt.timeIntervalSince1970)"
    }

    /// True when the rows are this truck's, from this charge: the card draws nothing from another truck's or an earlier charge's rows.
    func matches(vehicleID: String?, startedAt: Date?) -> Bool {
        guard let key = Self.owner(vehicleID, startedAt) else { return false }
        return owner == key
    }

    /// Reads the charge so far: a minute before it started to a minute from now. A failed read keeps what was there, unless it was another
    /// truck's or another charge's, which goes first.
    func load(source: some ChargeSamplesSource, vehicleID: String, startedAt: Date, now: Date = .now) async {
        if owner != Self.owner(vehicleID, startedAt) {
            owner = Self.owner(vehicleID, startedAt)
            series = ChargeCurve.Series()
            startPct = nil
            pack = nil
        }
        do {
            let rows = try await source.chargeSamples(since: startedAt.addingTimeInterval(-60), until: now.addingTimeInterval(60), vehicleID: vehicleID, limit: 2000)
            apply(rows, startedAt: startedAt, now: now)
        } catch {
            if Task.isCancelled { return }
        }
    }

    func apply(_ rows: [ChargeSample], startedAt: Date, now: Date) {
        let from = startedAt.addingTimeInterval(-60)
        let window = rows.filter { $0.at >= from }.sorted { $0.at < $1.at }
        series = ChargeCurve.series(rows, from: from, to: now)
        startPct = window.first { $0.batteryLevelPct.isFinite && $0.batteryLevelPct > 0 }?.batteryLevelPct
        let withPack = window.last { ($0.packTempAvgC?.isFinite ?? false) && ($0.packTempAvgC ?? 0) != 0 }
        pack = withPack.flatMap { s in s.packTempAvgC.map { ($0, s.at) } }
    }

    /// The pack's average, only while the newest reading is recent.
    func packTempC(now: Date) -> Double? {
        guard let pack, now.timeIntervalSince(pack.at) <= Self.packFresh else { return nil }
        return pack.avgC
    }
}
