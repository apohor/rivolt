import XCTest
@testable import Rivolt

private final class FakePva: PlanVsActualSource, @unchecked Sendable {
    var answers: [PlanVsActualAnswer] = []
    var failure: Error?
    private(set) var reads = 0
    private(set) var links: [(String, PlanVsActualLinkBody)] = []
    private(set) var unlinks: [(String, PlanVsActualLinkBody)] = []

    func planVsActual(savedID: String) async throws -> PlanVsActualAnswer {
        reads += 1
        if let failure { throw failure }
        return answers[min(reads - 1, answers.count - 1)]
    }
    func linkPlanVsActual(savedID: String, _ body: PlanVsActualLinkBody) async throws { links.append((savedID, body)); if let failure { throw failure } }
    func unlinkPlanVsActual(savedID: String, _ body: PlanVsActualLinkBody) async throws { unlinks.append((savedID, body)); if let failure { throw failure } }
}

private func pva(_ json: String) throws -> PlanVsActual { try JSON.decoder().decode(PlanVsActual.self, from: Data(json.utf8)) }

@MainActor
final class PlanVsActualTests: XCTestCase {
    func testALinkedTripReadsFromTheFixtureAndSaysOverOrUnderPlan() throws {
        let linked = try Fixture.decode(PlanVsActualAnswer.self, "trip_plan_actual_linked").parts[0]
        XCTAssertEqual(PlanVsActualLogic.state(linked), .linkedFull)
        let c = try XCTUnwrap(linked.comparison)
        XCTAssertEqual(PlanVsActualLogic.costHeader(c), "$2.45 over plan. Planned $12.75, paid $15.20.")
        let rows = PlanVsActualLogic.rows(c)
        XCTAssertEqual(rows.map(\.label), ["Distance", "Fast-charging cost"])
        XCTAssertEqual(rows[1].delta, "+$2.45")
        XCTAssertTrue(rows[1].watch, "more cost than planned")
        XCTAssertEqual(rows[1].reasons, ["The charger's price was higher than the estimate."])
        XCTAssertEqual(rows[0].planned, "278 mi")
        XCTAssertFalse(rows[0].watch, "distance is neutral")
    }

    func testNoDriveYetAndAMultiDayAnswerHaveTheirOwnStates() throws {
        let none = try Fixture.decode(PlanVsActualAnswer.self, "trip_plan_actual_none").parts[0]
        XCTAssertEqual(PlanVsActualLogic.state(none), .none)
        let multi = try Fixture.decode(PlanVsActualAnswer.self, "trip_plan_actual_multi").parts
        XCTAssertEqual(multi.map(\.day), [1, 2])
        XCTAssertEqual(multi.map(PlanVsActualLogic.state), [.linkedFull, .none])
    }

    func testASuggestionAsksAndAnUnfinishedTripIsNotCompared() throws {
        let suggested = try pva(#"{"status":"suggested","candidates":[{"trip_id":"o1","started_at":"2026-10-28T16:00:00Z","band":"medium","miles":281.4}],"comparison":null}"#)
        XCTAssertEqual(PlanVsActualLogic.state(suggested), .suggested)
        XCTAssertEqual(PlanVsActualLogic.question(suggested.candidates?.first, timeZone: TimeZone(identifier: "UTC")!), "Was this your trip on Oct 28?")
        XCTAssertEqual(PlanVsActualLogic.matchLabel("medium"), "Possible match")
        let going = try pva(#"{"status":"suggested","candidates":[{"trip_id":"o1","in_progress":true}],"comparison":null}"#)
        XCTAssertEqual(PlanVsActualLogic.state(going), .inProgress)
        let half = try pva(#"{"status":"linked","link":{"trip_id":"o1"},"candidates":[],"comparison":{"status":"in_progress","metrics":[]}}"#)
        XCTAssertEqual(PlanVsActualLogic.state(half), .inProgress, "a half-driven trip would read as saved money")
        let lost = try pva(#"{"status":"linked","link":{"trip_id":"o1"},"candidates":[],"comparison":null}"#)
        XCTAssertEqual(PlanVsActualLogic.state(lost), .linkLost)
    }

    func testAnIncompleteCostIsNeverShownAsTheActual() throws {
        let p = try pva(#"{"status":"linked","link":{"trip_id":"o1"},"comparison":{"status":"complete","currency":"USD","metrics":[{"metric":"cost","planned":12.75,"actual":null,"actual_complete":false,"actual_known":9.5,"unpriced_sessions":2,"reasons":[]}]}}"#)
        XCTAssertEqual(PlanVsActualLogic.state(p), .linkedPartial)
        let c = try XCTUnwrap(p.comparison)
        XCTAssertEqual(PlanVsActualLogic.costHeader(c), "Not final yet. $9.50 so far; 2 charges aren't priced yet.")
        let row = try XCTUnwrap(PlanVsActualLogic.rows(c).first)
        XCTAssertNil(row.actual)
        XCTAssertNil(row.delta)
        XCTAssertEqual(row.incomplete, "$9.50 so far; 2 charges aren't priced yet")
    }

    func testPlannedEnergyOfZeroIsMissingNotAPlan() throws {
        let p = try pva(#"{"status":"linked","link":{"trip_id":"o1"},"comparison":{"status":"complete","currency":"USD","metrics":[{"metric":"energy_kwh","planned":0,"actual":98.4,"delta":98.4,"reasons":[{"code":"extra_stop"}]}]}}"#)
        let row = try XCTUnwrap(PlanVsActualLogic.rows(try XCTUnwrap(p.comparison)).first)
        XCTAssertNil(row.planned)
        XCTAssertNil(row.delta)
        XCTAssertEqual(row.actual, "98 kWh")
        XCTAssertTrue(row.reasons.isEmpty)
    }

    func testLinkingConfirmsThenReadsAgainAndAFailureIsOurOwnWords() async throws {
        let src = FakePva()
        let suggested = try pva(#"{"status":"suggested","candidates":[{"trip_id":"o1"}],"comparison":null}"#)
        let linked = try Fixture.decode(PlanVsActualAnswer.self, "trip_plan_actual_linked")
        src.answers = [PlanVsActualAnswer(parts: [suggested]), linked]
        let m = PlanVsActualModel()
        await m.load(source: src, savedID: "s1")
        await m.link(source: src, savedID: "s1", tripID: "o1", day: nil)
        XCTAssertEqual(src.links.first?.1, PlanVsActualLinkBody(tripId: "o1", day: nil))
        XCTAssertEqual(src.reads, 2, "what shows is what the server holds")
        guard case .loaded(let parts) = m.phase else { return XCTFail("\(m.phase)") }
        XCTAssertEqual(PlanVsActualLogic.state(parts[0]), .linkedFull)
        src.failure = APIError.server(status: 500, message: "boom")
        await m.unlink(source: src, savedID: "s1", tripID: "o1", day: 2)
        XCTAssertEqual(m.message, "Couldn't reach the comparison right now. Try again in a minute.")
        XCTAssertEqual(src.reads, 2, "a failed change does not pretend to have changed")
    }

    func testTheLinkBodyIsSnakeCaseWithTheDayOnlyWhenGiven() throws {
        let enc = JSONEncoder()
        enc.keyEncodingStrategy = .convertToSnakeCase
        let one = String(decoding: try enc.encode(PlanVsActualLinkBody(tripId: "o1", day: nil)), as: UTF8.self)
        XCTAssertEqual(one, #"{"trip_id":"o1"}"#)
        let two = String(decoding: try enc.encode(PlanVsActualLinkBody(tripId: "o1", day: 2)), as: UTF8.self)
        XCTAssertTrue(two.contains(#""day":2"#), two)
    }
}
