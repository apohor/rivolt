import SwiftUI
import XCTest
@testable import Rivolt

private actor Counter {
    var calls = 0
    func bump() { calls += 1 }
}

private struct FakeSplit: PlacesSource {
    let counter = Counter()
    var result: Result<ChargeSplit, Error>
    func chargeSplit(since: Date, until: Date, vehicleID: String) async throws -> ChargeSplit {
        await counter.bump()
        return try result.get()
    }
}

final class PlacesMathTests: XCTestCase {
    func testTheFixtureDecodes() throws {
        let s = try Fixture.decode(ChargeSplit.self, "charge_split")
        XCTAssertEqual(s.home, .init(sessions: 18, energyKwh: 412.6))
        XCTAssertEqual(s.total, 25)
    }

    func testSharesAreWholePercentsThatAddUpToOneHundred() throws {
        let s = try Fixture.decode(ChargeSplit.self, "charge_split")
        // 64.4, 6.43 and 29.17 floor to 99; the biggest remainder gets the last point.
        XCTAssertEqual(PlacesMath.shareByEnergy([s.home.energyKwh, s.away.energyKwh, s.fast.energyKwh]), [64, 7, 29])
        XCTAssertEqual(PlacesMath.shareByEnergy([0, 0, 0]), [0, 0, 0])
        XCTAssertEqual(PlacesMath.shareByEnergy([1, 1, 1]).reduce(0, +), 100)
    }
}

@MainActor
final class PlacesModelTests: XCTestCase {
    private let m1 = PeriodMonth(containing: Date(timeIntervalSince1970: 1_800_000_000), calendar: Calendar(identifier: .gregorian))
    private let m2 = PeriodMonth(containing: Date(timeIntervalSince1970: 1_797_000_000), calendar: Calendar(identifier: .gregorian))

    private func split(_ n: Int) -> ChargeSplit {
        ChargeSplit(home: .init(sessions: n, energyKwh: 1), away: .init(sessions: 0, energyKwh: 0), fast: .init(sessions: 0, energyKwh: 0), total: n)
    }

    func testAnAnswerIsReusedForFifteenMinutesAndAPullReadsAgain() async {
        let src = FakeSplit(result: .success(split(1)))
        let model = PlacesModel()
        let t0 = Date(timeIntervalSince1970: 1_800_000_000)
        await model.load(source: src, vehicleID: "a", month: m1, now: t0)
        await model.load(source: src, vehicleID: "a", month: m1, now: t0.addingTimeInterval(14 * 60))
        var n = await src.counter.calls
        XCTAssertEqual(n, 1)
        await model.load(source: src, vehicleID: "a", month: m1, force: true, now: t0.addingTimeInterval(14 * 60))
        await model.load(source: src, vehicleID: "a", month: m1, now: t0.addingTimeInterval(16 * 60 + 14 * 60))
        n = await src.counter.calls
        XCTAssertEqual(n, 3)
    }

    func testARefusedReadKeepsWhatIsOnScreen() async {
        let ok = FakeSplit(result: .success(split(5)))
        let model = PlacesModel()
        await model.load(source: ok, vehicleID: "a", month: m1)
        let limited = FakeSplit(result: .failure(APIError.server(status: 429, message: "slow down")))
        await model.load(source: limited, vehicleID: "a", month: m1, force: true)
        XCTAssertEqual(model.split?.total, 5)
    }

    func testAnotherPeriodNeverShowsThisPeriodsFigures() async {
        let ok = FakeSplit(result: .success(split(5)))
        let model = PlacesModel()
        await model.load(source: ok, vehicleID: "a", month: m1)
        let down = FakeSplit(result: .failure(APIError.transport("down")))
        await model.load(source: down, vehicleID: "a", window: .month)
        XCTAssertNil(model.split)
    }
}
