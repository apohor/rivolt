import SwiftUI
import XCTest
@testable import Rivolt

final class AppearanceChoiceTests: XCTestCase {
    func testEachChoiceMapsToItsScheme() {
        XCTAssertNil(AppearanceChoice.system.scheme)
        XCTAssertEqual(AppearanceChoice.light.scheme, .light)
        XCTAssertEqual(AppearanceChoice.dark.scheme, .dark)
    }

    func testTheSavedValueRoundTripsAndAnUnknownOneIsNotAChoice() {
        for c in AppearanceChoice.allCases { XCTAssertEqual(AppearanceChoice(rawValue: c.rawValue), c) }
        XCTAssertNil(AppearanceChoice(rawValue: "sepia"))
        XCTAssertEqual(AppearanceChoice.allCases.map(\.title), ["System", "Light", "Dark"])
    }

    func testTheAutoNameIsOnlyForTheThemeSwitch() {
        XCTAssertEqual(AppearanceChoice.allCases.map { $0.title(themeSwitch: true) }, ["Auto", "Light", "Dark"])
        XCTAssertEqual(AppearanceChoice.allCases.map { $0.title(themeSwitch: false) }, ["System", "Light", "Dark"])
    }

    func testDesignPreviewKeysDecodeAndAnythingElseMeansNone() throws {
        func keys(_ json: String) throws -> [String]? { try JSON.decoder().decode(MapsConfig.self, from: Data(json.utf8)).features?.designPreview }
        XCTAssertEqual(try keys(#"{"features":{"design_preview":["theme_switch","now_v2"]}}"#), ["theme_switch", "now_v2"])
        XCTAssertEqual(try keys(#"{"features":{"design_preview":[]}}"#), [])
        XCTAssertNil(try keys(#"{"features":{}}"#), "an older server omits it")
        XCTAssertNil(try keys(#"{"features":{"design_preview":"theme_switch"}}"#), "not a list")
        XCTAssertNil(try keys(#"{"tiles":{}}"#))
    }
}
