import Foundation
import Observation

/// The drive so far for the Now card (design_preview key now_drive_graph): the stored samples from the start of the drive (`/api/samples`, no Rivian
/// call), read again at the card's own 30 s pace by the caller while the screen is up; nothing is kept past the screen. A failed read keeps what
/// was there, unless it was another truck's or another drive's.
@MainActor
@Observable
final class NowDriveSeriesModel {
    static let refresh: TimeInterval = 30
    /// A start this far from the one held is another drive (the start is worked out from the elapsed time at each read, so it wanders by seconds).
    private static let sameDrive: TimeInterval = 120

    private(set) var samples: [DriveSample] = []
    private(set) var vehicleID: String?
    private(set) var startedAt: Date?

    /// True when the rows are this truck's, from this drive.
    func matches(vehicleID id: String?, startedAt start: Date?) -> Bool {
        guard let id, let start, vehicleID == id, let held = startedAt else { return false }
        return abs(held.timeIntervalSince(start)) < Self.sameDrive
    }

    func load(source: some DriveSamplesSource, vehicleID id: String, startedAt start: Date, now: Date = .now) async {
        if !matches(vehicleID: id, startedAt: start) {
            vehicleID = id
            startedAt = start
            samples = []
        }
        do {
            samples = try await source.driveSamples(since: start.addingTimeInterval(-30), until: now.addingTimeInterval(60), vehicleID: id, limit: 2000)
        } catch {
            if Task.isCancelled { return }
        }
    }

    func clear() { samples = []; vehicleID = nil; startedAt = nil }
}

/// The one read behind the drive's graph, apart from `APIClient` so tests can see what is asked for.
protocol DriveSamplesSource: Sendable {
    func driveSamples(since: Date, until: Date, vehicleID: String, limit: Int) async throws -> [DriveSample]
}

extension APIClient: DriveSamplesSource {}
