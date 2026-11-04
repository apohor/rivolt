import SwiftUI
import XCTest
@testable import Rivolt

@MainActor
final class DrivingBudgetTests: XCTestCase {
    override func setUp() { UserDefaults.standard.removeObject(forKey: DrivingBudget.key) }
    override func tearDown() { UserDefaults.standard.removeObject(forKey: DrivingBudget.key) }

    private func route(driveSec: Double?) throws -> TripRoute {
        var r = try XCTUnwrap(try Fixture.decode(TripPlan.self, "trip_plan").best)
        r.totalDriveDurationSec = driveSec
        return r
    }

    func testEightHoursUntilChangedAndEveryUseAgrees() throws {
        XCTAssertEqual(DrivingBudget.hours, 8)
        let r = try route(driveSec: 9 * 3600)
        XCTAssertTrue(TripPlanLogic.isLongDrive(r))
        XCTAssertEqual(TripPlanLogic.overnightRequest(r).budgetHours, 8)
        UserDefaults.standard.set(10.0, forKey: DrivingBudget.key)
        XCTAssertFalse(TripPlanLogic.isLongDrive(r), "9 h fits a 10 h day")
        XCTAssertEqual(TripPlanLogic.overnightRequest(r).budgetHours, 10)
        UserDefaults.standard.set(6.0, forKey: DrivingBudget.key)
        XCTAssertTrue(TripPlanLogic.isLongDrive(r))
        XCTAssertEqual(TripPlanLogic.overnightRequest(r).budgetHours, 6)
        XCTAssertEqual(TripPlanLogic.longDriveSeconds, 6 * 3600)
    }

    func testTheLimitStaysInRangeOnWholeHours() {
        XCTAssertEqual(DrivingBudget.clamped(1), 4)
        XCTAssertEqual(DrivingBudget.clamped(20), 14)
        XCTAssertEqual(DrivingBudget.clamped(7.3), 7)
        XCTAssertEqual(DrivingBudget.clamped(.nan), 8)
        UserDefaults.standard.set(99.0, forKey: DrivingBudget.key)
        XCTAssertEqual(DrivingBudget.hours, 14, "a bad stored value is kept in range")
        XCTAssertEqual(DrivingBudget.label(8), "8 h")
    }

    func testTheNoStaysMessageNamesTheSameLimit() throws {
        UserDefaults.standard.set(6.0, forKey: DrivingBudget.key)
        let found = try JSON.decoder().decode(OvernightCandidates.self, from: Data(#"{"candidates":[],"target":{"along_m":300000,"window_before_min":30,"window_after_min":30},"route_m":600000}"#.utf8))
        XCTAssertTrue(TripPlanLogic.noStaysLine(found, driveSec: 36_000, metric: false).contains("your 6 h point"))
    }

    func testOnlyADriveLongerThanTheShortestLimitAsks() throws {
        XCTAssertFalse(TripPlanLogic.offersDrivingPerDay(try route(driveSec: 3 * 3600)))
        XCTAssertTrue(TripPlanLogic.offersDrivingPerDay(try route(driveSec: 5 * 3600)))
    }

    func testRenderTheRowAtALargeTextSize() throws {
        Theme.registerFonts()
        for scheme in [ColorScheme.light, .dark] {
            for (label, type) in [("default", DynamicTypeSize.large), ("ax3", .accessibility3)] {
                let page = DrivingPerDayRow(model: TripPlanModel()).padding(12).frame(width: 367).background(ThemePageBackground())
                    .environment(\.colorScheme, scheme).environment(\.dynamicTypeSize, type)
                let renderer = ImageRenderer(content: page)
                renderer.scale = 2
                let png = try XCTUnwrap(try XCTUnwrap(renderer.uiImage, "row did not render").pngData())
                let file = "driving-per-day-\(scheme == .dark ? "dark" : "light")-\(label).png"
                let attachment = XCTAttachment(data: png, uniformTypeIdentifier: "public.png")
                attachment.name = file
                attachment.lifetime = .keepAlways
                add(attachment)
                if let dir = ProcessInfo.processInfo.environment["SNAPSHOT_DIR"], !dir.isEmpty {
                    try FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
                    try png.write(to: URL(fileURLWithPath: dir).appendingPathComponent(file))
                }
            }
        }
    }
}
