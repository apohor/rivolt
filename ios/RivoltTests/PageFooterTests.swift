import XCTest
@testable import Rivolt

final class PageFooterTests: XCTestCase {
    private let server = URL(string: "https://rivolt.dev")!

    func testServerPagesOpenOnTheUsersOwnServerAndTheRestAreFixedHttpsAddresses() throws {
        let urls = Dictionary(uniqueKeysWithValues: PageFooter.items.map { ($0.label, PageFooter.url(for: $0, server: server)) })
        XCTAssertEqual(urls["Privacy"], URL(string: "https://rivolt.dev/privacy"))
        XCTAssertEqual(urls["Terms"], URL(string: "https://rivolt.dev/terms"))
        XCTAssertEqual(urls["Cost to charge"], URL(string: "https://rivolt.dev/charging-cost"))
        XCTAssertEqual(urls["GitHub"], URL(string: "https://github.com/apohor/rivolt"))
        XCTAssertEqual(urls["Discord"], URL(string: "https://discord.gg/FGty2xmdeV"))
        XCTAssertEqual(urls["MIT licensed"] ?? nil, nil, "a label, not a link")
        // A self-hosted server is the base for its own pages.
        let own = URL(string: "https://rivian.example.org:8443")!
        let privacy = try XCTUnwrap(PageFooter.items.first { $0.label == "Privacy" })
        XCTAssertEqual(PageFooter.url(for: privacy, server: own), URL(string: "https://rivian.example.org:8443/privacy"))
    }

    func testEveryLinkIsHttpsOnTheServerOrANamedExternalHost() {
        for item in PageFooter.items {
            guard let u = PageFooter.url(for: item, server: server) else { continue }
            XCTAssertEqual(u.scheme, "https", item.label)
            XCTAssertTrue(["rivolt.dev", "github.com", "discord.gg"].contains(u.host ?? ""), "\(item.label) goes to \(u.host ?? "?")")
        }
    }

    func testTheVersionIsTheAppsOwn() {
        let v = PageFooter.versionText()
        XCTAssertNotNil(v)
        XCTAssertTrue(v?.hasPrefix("App v") == true, v ?? "")
    }
}
