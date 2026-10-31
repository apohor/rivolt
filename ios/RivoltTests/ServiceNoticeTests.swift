import XCTest
@testable import Rivolt

final class ServiceNoticeTests: XCTestCase {
    private let server = URL(string: "https://rivolt.dev")!

    private func decode(_ json: String) throws -> [ServiceNotice] {
        let d = JSONDecoder()
        d.keyDecodingStrategy = .convertFromSnakeCase
        return try d.decode([ServiceNotice].self, from: Data(json.utf8))
    }

    func testDecodesTheServersShapeAndIgnoresShowsUntil() throws {
        let list = try decode("""
        [{"kind":"pro_launch","title":"Rivolt Pro starts on a date","body":"What stays free.",
          "links":[{"label":"Read more","href":"/support"}],"shows_until":"2027-01-01T00:00:00Z","dismissed":false},
         {"kind":"founding_end","title":"T","body":"B","links":[],"shows_until":"2027-02-01T00:00:00.5Z","dismissed":true}]
        """)
        XCTAssertEqual(list.map(\.kind), ["pro_launch", "founding_end"])
        XCTAssertEqual(list[0].links, [ServiceNotice.Link(label: "Read more", href: "/support")])
        XCTAssertEqual(NoticeLogic.banner(list)?.kind, "pro_launch")
        XCTAssertNil(NoticeLogic.banner([list[1]]))
        XCTAssertNil(NoticeLogic.banner([]))
    }

    func testOnlyPathsAndHttpsAreLinks() {
        XCTAssertEqual(NoticeLogic.url("/support", server: server)?.absoluteString, "https://rivolt.dev/support")
        XCTAssertEqual(NoticeLogic.url("https://example.org/docs", server: server)?.absoluteString, "https://example.org/docs")
        XCTAssertNil(NoticeLogic.url("//evil.example", server: server))
        XCTAssertNil(NoticeLogic.url("http://example.org", server: server))
        XCTAssertNil(NoticeLogic.url("javascript:alert(1)", server: server))
        XCTAssertNil(NoticeLogic.url("not a url", server: server))
    }

    func testSettingsListsNoticesOnlyWhenThereAreSome() {
        func account(_ on: Bool) -> [SettingsLogic.Row] {
            SettingsLogic.groups(notificationsOffered: false, noticesOffered: on).first { $0.title == "Account" }!.rows
        }
        XCTAssertTrue(account(true).contains(.notices))
        XCTAssertFalse(account(false).contains(.notices))
    }
}
