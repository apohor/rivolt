import XCTest
@testable import Rivolt

private final class FakePasskeys: PasskeysSource, @unchecked Sendable {
    var result: Result<PasskeyList, Error> = .success(PasskeyList())
    func passkeys() async throws -> PasskeyList { try result.get() }
}

@MainActor
final class PasskeysTests: XCTestCase {
    private let json = #"{"passkeys":[{"id":"00000000-0000-4000-8000-0000000000b1","label":"Example iPhone","created_at":"2026-10-28T16:00:00Z","last_used_at":null,"synced":true},{"id":"00000000-0000-4000-8000-0000000000b2","label":"Example key","created_at":"2026-10-30T09:00:00Z","last_used_at":"2026-11-02T10:00:00Z","synced":false}],"max":10}"#

    private func list() throws -> PasskeyList { try JSON.decoder().decode(PasskeyList.self, from: Data(json.utf8)) }

    func testTheListReadsAsTheServerSendsItAndSaysWhereEachIsKept() throws {
        let l = try list()
        XCTAssertEqual(l.passkeys.map(\.label), ["Example iPhone", "Example key"])
        XCTAssertEqual(l.max, 10)
        XCTAssertEqual(PasskeysModel.kind(l.passkeys[0]), "Synced")
        XCTAssertEqual(PasskeysModel.kind(l.passkeys[1]), "This device")
    }

    func testEachPasskeySaysWhenItWasAddedAndWhenItWasLastUsed() throws {
        let l = try list()
        let utc = TimeZone(identifier: "UTC")!
        let now = ISO8601DateFormatter().date(from: "2026-11-03T12:00:00Z")!
        XCTAssertEqual(PasskeysModel.detail(l.passkeys[0], now: now, timeZone: utc), "Added Oct 28 · not used yet")
        XCTAssertEqual(PasskeysModel.detail(l.passkeys[1], now: now, timeZone: utc), "Added Oct 30 · last used Nov 2")
    }

    func testLoadingShowsTheListAndOffOrSignedOutHidesTheSectionWhileAFailureIsSaid() async throws {
        let src = FakePasskeys()
        src.result = .success(try list())
        let m = PasskeysModel()
        await m.load(source: src)
        guard case .loaded(let l) = m.phase else { return XCTFail("\(m.phase)") }
        XCTAssertEqual(l.passkeys.count, 2)
        src.result = .failure(APIError.server(status: 500, message: "boom"))
        await m.load(source: src)
        guard case .loaded = m.phase else { return XCTFail("a failed refresh keeps the list: \(m.phase)") }
        for status in [401, 403, 404] {
            let off = PasskeysModel()
            src.result = .failure(APIError.server(status: status, message: "x"))
            await off.load(source: src)
            XCTAssertEqual(off.phase, .off, "\(status): passkeys are not for this server or this sign-in")
        }
        let broken = PasskeysModel()
        src.result = .failure(APIError.server(status: 500, message: "boom"))
        await broken.load(source: src)
        XCTAssertEqual(broken.phase, .failed("Couldn't load your passkeys. Try again in a minute."))
    }
}
