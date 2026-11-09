import XCTest
@testable import Rivolt

/// ios/design/vehicle-discovery.json is generated from the web's words and VehicleDiscoveryCopy.swift from it; and what each state of the server's answer shows.
final class VehicleDiscoveryTests: XCTestCase {
    private struct Doc: Decodable {
        struct T: Decodable { let title: String; let body: String; let action: String? }
        let looking: T
        let none: T
        let reauth: T
        let check: String
    }

    func testTheSwiftMatchesTheJSON() throws {
        let url = try XCTUnwrap(Bundle(for: Self.self).url(forResource: "vehicle-discovery", withExtension: "json"), "vehicle-discovery.json is not in the test bundle")
        let doc = try JSONDecoder().decode(Doc.self, from: Data(contentsOf: url))
        XCTAssertEqual(VehicleDiscoveryCopy.lookingTitle, doc.looking.title)
        XCTAssertEqual(VehicleDiscoveryCopy.lookingBody, doc.looking.body)
        XCTAssertEqual(VehicleDiscoveryCopy.noneTitle, doc.none.title)
        XCTAssertEqual(VehicleDiscoveryCopy.noneBody, doc.none.body)
        XCTAssertEqual(VehicleDiscoveryCopy.reauthTitle, doc.reauth.title)
        XCTAssertEqual(VehicleDiscoveryCopy.reauthBody, doc.reauth.body)
        XCTAssertEqual(VehicleDiscoveryCopy.reauthAction, doc.reauth.action)
        XCTAssertEqual(VehicleDiscoveryCopy.check, doc.check)
    }

    func testTheServersAnswerWinsAndA503IsLooking() throws {
        for (word, state) in [("unlinked", VehicleDiscoveryState.unlinked), ("reauth", .reauth), ("found", .found), ("none", .empty), ("looking", .looking)] {
            XCTAssertEqual(VehicleDiscoveryState.from(VehicleDiscovery(state: word), error: nil), state)
        }
        XCTAssertEqual(VehicleDiscoveryState.from(nil, error: APIError.server(status: 503, message: "")), .looking)
        XCTAssertNil(VehicleDiscoveryState.from(nil, error: APIError.server(status: 404, message: "")), "a server without the feature: the older screen")
        XCTAssertNil(VehicleDiscoveryState.from(nil, error: URLError(.notConnectedToInternet)))
        XCTAssertNil(VehicleDiscoveryState.from(VehicleDiscovery(state: "nonsense"), error: nil))
    }

    func testEachStateShowsItsOwnWords() {
        XCTAssertEqual(VehicleDiscoveryState.looking.view?.title, "Looking for your vehicles")
        XCTAssertNil(VehicleDiscoveryState.looking.view?.action)
        XCTAssertEqual(VehicleDiscoveryState.empty.view?.title, "No vehicles on this Rivian account yet")
        XCTAssertEqual(VehicleDiscoveryState.reauth.view?.title, "Reconnect your Rivian")
        XCTAssertEqual(VehicleDiscoveryState.reauth.view?.action, "Reconnect Rivian")
        XCTAssertNil(VehicleDiscoveryState.unlinked.view, "the connect screen already says it")
        XCTAssertNil(VehicleDiscoveryState.found.view)
    }
}
