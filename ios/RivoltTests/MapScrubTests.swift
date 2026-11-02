import XCTest
@testable import Rivolt

/// The drive map's scrub: the dot follows the timeline's cursor along the recorded fixes.
@MainActor
final class MapScrubTests: XCTestCase {
    private func sample(_ at: String, _ lat: Double, _ lon: Double) -> DriveSample {
        DriveSample(at: JSON.parseDate(at)!, lat: lat, lon: lon, speedMph: 30, shiftState: "D", locationFixAt: nil)
    }

    private func at(_ s: String) -> Date { JSON.parseDate(s)! }

    private var samples: [DriveSample] {
        [sample("2026-10-20T15:00:00Z", 10.0, 20.0), sample("2026-10-20T15:01:00Z", 10.2, 20.2), sample("2026-10-20T15:01:30Z", 10.3, 20.3),
         // A 10-minute hole in the recording, then more.
         sample("2026-10-20T15:11:30Z", 11.0, 21.0)]
    }

    func testTheDotSitsBetweenTheTwoFixesInProportionToTime() throws {
        let c = try XCTUnwrap(RouteSnap.cursorCoord(at: at("2026-10-20T15:00:30Z"), samples: samples))
        XCTAssertEqual(c.lat, 10.1, accuracy: 1e-9)
        XCTAssertEqual(c.lon, 20.1, accuracy: 1e-9)
    }

    func testBeforeTheFirstAndAfterTheLastFixItStaysOnTheEnds() throws {
        XCTAssertEqual(try XCTUnwrap(RouteSnap.cursorCoord(at: at("2026-10-20T14:00:00Z"), samples: samples)).lat, 10.0)
        XCTAssertEqual(try XCTUnwrap(RouteSnap.cursorCoord(at: at("2026-10-20T16:00:00Z"), samples: samples)).lat, 11.0)
    }

    func testItNeverGlidesAcrossAHoleInTheRecording() throws {
        // 15:03 is closer to the fix before the hole, 15:09 to the one after.
        XCTAssertEqual(try XCTUnwrap(RouteSnap.cursorCoord(at: at("2026-10-20T15:03:00Z"), samples: samples)).lat, 10.3)
        XCTAssertEqual(try XCTUnwrap(RouteSnap.cursorCoord(at: at("2026-10-20T15:09:00Z"), samples: samples)).lat, 11.0)
    }

    func testFixesWithoutAPositionAreSkippedAndNoFixIsNil() {
        let none = [sample("2026-10-20T15:00:00Z", 0, 0), sample("2026-10-20T15:01:00Z", 200, 0)]
        XCTAssertNil(RouteSnap.cursorCoord(at: at("2026-10-20T15:00:30Z"), samples: none))
        XCTAssertNil(RouteSnap.cursorCoord(at: at("2026-10-20T15:00:30Z"), samples: []))
    }

    func testTheMapChipReadsLikeTheTimelinesOwnReadout() throws {
        var utc = Calendar(identifier: .gregorian)
        utc.timeZone = TimeZone(identifier: "UTC")!
        let row = try XCTUnwrap(Fixture.decode(ActivityResponse.self, "activity").drives.first { $0.id == "drv-3" })
        let n = Int(row.endedAt.timeIntervalSince(row.startedAt) / 10)
        let samples = (0...n).map { i in
            DriveSample(at: row.startedAt.addingTimeInterval(Double(i) * 10), lat: 32.7767 + Double(i) * 1e-4, lon: -96.797, speedMph: 30,
                        shiftState: "D", locationFixAt: nil, batteryLevelPct: 80, altitudeM: 150, driveMode: "everyday")
        }
        let data = try XCTUnwrap(DriveTimelineData(drive: row, samples: samples))
        let text = DriveTimelineCard.readout(data, at: row.startedAt.addingTimeInterval(120), calendar: utc, metric: false)
        XCTAssertTrue(text.contains("30 mph"), text)
        XCTAssertTrue(text.contains("battery 80%"), text)
        XCTAssertTrue(DriveTimelineCard.readout(data, at: row.startedAt.addingTimeInterval(120), calendar: utc, metric: true).contains("48 km/h"))
    }
}
