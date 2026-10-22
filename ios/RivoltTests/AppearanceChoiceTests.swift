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
}
