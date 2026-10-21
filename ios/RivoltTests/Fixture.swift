import Foundation
import XCTest
@testable import Rivolt

/// Server-response fixtures shared with Go: internal/api/now_ios_fixture_test.go
/// decodes the same files strictly into the server's types.
enum Fixture {
    static func data(_ name: String) throws -> Data {
        let bundle = Bundle(for: BundleToken.self)
        let url = try XCTUnwrap(bundle.url(forResource: name, withExtension: "json"), "missing fixture \(name).json")
        return try Data(contentsOf: url)
    }

    static func decode<T: Decodable>(_ type: T.Type, _ name: String) throws -> T {
        try JSON.decoder().decode(T.self, from: data(name))
    }

    private final class BundleToken {}
}
