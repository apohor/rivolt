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

    /// One drive of a merged round trip, with its own route and samples (a leg's window, 10 minutes either side).
    struct Leg: Equatable {
        let leg: ActivityLeg
        let route: DriveRoute
        let samples: [DriveSample]?
    }

    private(set) var phase = Phase.loading
    /// Every leg of a merged round trip, in order; empty for a single drive (the route map then draws `phase`).
    private(set) var legs: [Leg] = []
    /// The timeline's figures, worked out once when the samples arrive.
    private(set) var timeline: DriveTimelineData?

    /// The samples cover the drive's span (a round trip's too, from the Activity row), and 10 minutes either side for
    /// the route's parked pins, as the web's detail page.
    func load(source: some DriveDetailSource, drive row: ActivityDrive) async {
        phase = .loading
        timeline = nil
        legs = []
        if let list = row.legs, list.count > 1 {
            await loadLegs(source: source, row: row, list: list)
            return
        }
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

    /// A merged round trip: each leg's route and samples read on their own (the row carries only the first leg's id), then
    /// the legs' samples joined for the timeline and the cursor.
    private func loadLegs(source: some DriveDetailSource, row: ActivityDrive, list: [ActivityLeg]) async {
        let loaded = await withTaskGroup(of: (Int, Leg?).self) { group -> [Leg] in
            for (i, leg) in list.enumerated() {
                group.addTask {
                    guard let route = try? await source.driveRoute(id: leg.id) else { return (i, nil) }
                    var samples: [DriveSample]?
                    if let vehicle = route.vehicleID, !vehicle.isEmpty {
                        let start = min(route.startedAt ?? leg.startedAt, leg.startedAt), end = max(route.endedAt ?? leg.endedAt, leg.endedAt)
                        samples = try? await source.driveSamples(since: start.addingTimeInterval(-600), until: end.addingTimeInterval(600), vehicleID: vehicle, limit: 2000)
                    }
                    return (i, Leg(leg: leg, route: route, samples: samples))
                }
            }
            var out: [(Int, Leg)] = []
            for await (i, leg) in group { if let leg { out.append((i, leg)) } }
            return out.sorted { $0.0 < $1.0 }.map(\.1)
        }
        guard let first = loaded.first else {
            phase = .failed
            return
        }
        // The legs' windows overlap by their 10-minute margins: one sample per moment.
        var seen = Set<TimeInterval>()
        let all = loaded.compactMap(\.samples).flatMap { $0 }.filter { seen.insert($0.at.timeIntervalSince1970).inserted }.sorted { $0.at < $1.at }
        let samples: [DriveSample]? = loaded.contains { $0.samples != nil } ? all : nil
        legs = loaded
        timeline = samples.flatMap { DriveTimelineData(drive: row, samples: $0) }
        phase = .loaded(first.route, samples: samples)
    }
}
