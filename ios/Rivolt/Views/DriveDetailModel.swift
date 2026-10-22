import Foundation
import Observation

/// The two reads behind a drive's screen, kept apart from `APIClient` so tests can fake them.
protocol DriveDetailSource: Sendable {
    func driveRoute(id: String) async throws -> DriveRoute
    func driveSamples(since: Date, until: Date, vehicleID: String, limit: Int) async throws -> [DriveSample]
}

extension APIClient: DriveDetailSource {}

/// A drive's row and its samples, read once for the screen and shared by the route map and the timeline (one
/// /api/samples read, not two). Held while the screen is open, gone with it.
@MainActor
@Observable
final class DriveDetailModel {
    enum Phase: Equatable {
        case loading
        /// The drive couldn't be read.
        case failed
        /// The drive, and its samples (nil when they couldn't be read: the map falls back to the stored route).
        case loaded(DriveRoute, samples: [DriveSample]?)
    }

    private(set) var phase = Phase.loading
    /// The timeline's figures, worked out once when the samples arrive.
    private(set) var timeline: DriveTimelineData?

    /// The samples cover the drive's span (a round trip's too, from the Activity row), and 10 minutes either side for
    /// the route's parked pins, as the web's detail page.
    func load(source: some DriveDetailSource, drive row: ActivityDrive) async {
        phase = .loading
        timeline = nil
        let drive: DriveRoute
        do {
            drive = try await source.driveRoute(id: row.id)
        } catch {
            phase = .failed
            return
        }
        var samples: [DriveSample]?
        if let vehicle = drive.vehicleID, !vehicle.isEmpty {
            let start = min(drive.startedAt ?? row.startedAt, row.startedAt), end = max(drive.endedAt ?? row.endedAt, row.endedAt)
            samples = try? await source.driveSamples(since: start.addingTimeInterval(-600), until: end.addingTimeInterval(600), vehicleID: vehicle, limit: 2000)
        }
        timeline = samples.flatMap { DriveTimelineData(drive: row, samples: $0) }
        phase = .loaded(drive, samples: samples)
    }
}
