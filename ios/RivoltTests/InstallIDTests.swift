import XCTest
@testable import Rivolt

final class InstallIDTests: XCTestCase {
    private let service = "dev.rivolt.install.tests"

    override func setUp() { InstallID.reset(service: service) }
    override func tearDown() { InstallID.reset(service: service) }

    func testItIsRandomHexWithinTheServersLimit() {
        let a = InstallID.make(), b = InstallID.make()
        XCTAssertEqual(a.count, InstallID.length)
        XCTAssertLessThanOrEqual(a.count, 128)
        XCTAssertNotNil(a.range(of: "^[0-9a-f]+$", options: .regularExpression))
        XCTAssertNotEqual(a, b)
    }

    func testItIsMadeOnceAndKept() throws {
        let first = try XCTUnwrap(InstallID.current(service: service))
        XCTAssertEqual(InstallID.current(service: service), first)
    }
}
