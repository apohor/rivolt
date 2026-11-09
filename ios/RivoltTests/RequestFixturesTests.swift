import XCTest
@testable import Rivolt

/// The JSON the app sends, written down: one file per kind of request in ios/RivoltTests/RequestFixtures/, produced by the encoder the app really uses
/// (JSON.requestEncoder), and a test that the checked-in file equals what the encoder gives now. The server's tests read the same files, so an older build's
/// requests are still accepted after a server change, and a change to what the app sends shows up in a diff.
///
/// A change on purpose: `RECORD_REQUEST_FIXTURES=1` (as TEST_RUNNER_RECORD_REQUEST_FIXTURES for xcodebuild) rewrites the files, and the diff is the review.
final class RequestFixturesTests: XCTestCase {
    private func samples() throws -> [(name: String, body: any Encodable)] {
        let place = TripPoint(lat: 47.6205, lon: -122.3493, label: "Example Origin", entityId: nil)
        let end = TripPoint(lat: 47.6588, lon: -117.426, label: "Example Destination", entityId: nil)
        let saved = SavedTripBody(
            name: "Example trip",
            inputs: .object(["origin": .object(["label": .string("Example Origin"), "lat": .number(47.6205), "lon": .number(-122.3493)]),
                             "destination": .object(["label": .string("Example Destination"), "lat": .number(47.6588), "lon": .number(-117.426)]),
                             "pack_kwh": .number(135), "starting_soc": .number(88)]),
            plan: .object(["Status": .string("SUCCESS")]))
        return [
            ("trip_plan_request", TripPlanRequest(vehicleId: "veh-example", startingSoc: 88, originBearing: 90, departAt: "2027-01-12T15:00:00Z",
                waypoints: [TripPlanWaypoint(latitude: 47.6205, longitude: -122.3493, waypointType: "origin", entityId: nil),
                            TripPlanWaypoint(latitude: 47.6588, longitude: -117.426, waypointType: "destination", entityId: nil)],
                targetArrivalSocPercent: 10, driveMode: "EVERYDAY", hasAdapter: true, packKwh: 135)),
            ("trip_multiday_request", TripMultidayRequest(vehicleId: "veh-example", startingSoc: 88, packKwh: 135, startAt: "2027-01-12T15:00:00Z", tz: "America/Los_Angeles",
                targetArrivalSocPercent: 10, origin: place,
                days: [TripDayRequest(vias: nil, end: end, targetArrivalSocPct: 20, overnight: .init(l2Kw: 7, until: "08:00", maxSocPct: 90, name: "Example Inn"))],
                maxOvernightSocPct: 90)),
            ("saved_trip_body", saved),
            ("charging_rates_patch", ChargingRatesPatch(homePricePerKwh: 0.15, homeCurrency: "USD", gasPricePerGallon: nil, comparisonMpg: nil)),
            ("offpeak_put", OffpeakPut(tz: "America/Los_Angeles", windows: [OffpeakWindow(days: "all", start: "21:00", end: "06:00", rate: 0.09, label: nil)])),
            ("app_token_body", APIClient.AppTokenBody(code: "example-code", codeVerifier: "example-verifier", deviceName: "Example phone")),
            ("corridor_layers_plain", CorridorLayersRequest(path: [[47.6205, -122.3493], [47.6588, -117.426]], kinds: ["dcfc", "hotels"], limit: nil)),
            ("corridor_layers_settings", CorridorLayersRequest(path: [[47.6205, -122.3493], [47.6588, -117.426]], kinds: ["dcfc", "hotels"], limit: nil,
                                                               preset: "wide", startExcludeMi: 25, minFastKw: 150)),
            ("planner_prefs_old", PlannerPrefs(driveMode: "EVERYDAY", hasAdapter: true)),
            ("planner_prefs_full", PlannerPrefs(driveMode: "EVERYDAY", hasAdapter: true, corridor: "normal", startExcludeMi: 0, minFastKw: 0,
                                                layersOn: ["dcfc", "hotels"], preferredOnly: false, weatherOn: true, radarOn: false)),
        ]
    }

    private func encoded(_ body: any Encodable) throws -> String {
        let enc = JSON.requestEncoder()
        enc.outputFormatting = [.sortedKeys, .prettyPrinted]
        return String(data: try enc.encode(AnyEncodableBody(body)), encoding: .utf8)! + "\n"
    }

    /// Where the checked-in files are, from this file's place on the disk (for recording).
    private var sourceDirectory: URL {
        URL(fileURLWithPath: #filePath).deletingLastPathComponent().appendingPathComponent("RequestFixtures")
    }

    func testTheCheckedInRequestsAreWhatTheAppSends() throws {
        let record = ProcessInfo.processInfo.environment["RECORD_REQUEST_FIXTURES"] != nil
        for (name, body) in try samples() {
            let json = try encoded(body)
            if record {
                try FileManager.default.createDirectory(at: sourceDirectory, withIntermediateDirectories: true)
                try json.write(to: sourceDirectory.appendingPathComponent(name + ".json"), atomically: true, encoding: .utf8)
                continue
            }
            let url = try XCTUnwrap(Bundle(for: Self.self).url(forResource: name, withExtension: "json"), "RequestFixtures/\(name).json is not checked in: run with RECORD_REQUEST_FIXTURES=1")
            XCTAssertEqual(try String(contentsOf: url, encoding: .utf8), json, "\(name): what the app sends changed. If on purpose, record it (RECORD_REQUEST_FIXTURES=1) and review the diff, and check the server still accepts it.")
        }
    }

    func testEveryCheckedInRequestHasASample() throws {
        let names = Set(try samples().map(\.name))
        let dir = sourceDirectory
        let files = (try? FileManager.default.contentsOfDirectory(atPath: dir.path)) ?? []
        for f in files where f.hasSuffix(".json") {
            XCTAssertTrue(names.contains(String(f.dropLast(5))), "\(f) is checked in but no test encodes it: it would rot")
        }
    }
}

/// Lets any request body be encoded by the app's own encoder.
private struct AnyEncodableBody: Encodable {
    let wrapped: any Encodable
    init(_ wrapped: any Encodable) { self.wrapped = wrapped }
    func encode(to encoder: Encoder) throws { try wrapped.encode(to: encoder) }
}
