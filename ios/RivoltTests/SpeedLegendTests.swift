import XCTest
@testable import Rivolt

final class SpeedLegendTests: XCTestCase {
    private func piece(_ speed: RouteSnap.Speed?) -> RouteSnap.Piece {
        RouteSnap.Piece(coords: [RouteSnap.Coord(lat: 45.5, lon: -122.6), RouteSnap.Coord(lat: 45.6, lon: -122.5)], speed: speed)
    }

    func testTheLegendNamesABandByItsLowerBound() {
        XCTAssertEqual(RouteSnap.Speed.allCases.map(\.shortLabel), ["<5", "5", "25", "50", "65+"])
        XCTAssertEqual(RouteSnap.Speed.over65.label, "65+", "the full range is still there for VoiceOver")
        XCTAssertEqual(RouteSnap.Speed.to25.label, "5–25")
    }

    func testOnlyTheBandsADriveUsesAreListedSlowestFirst() {
        let d = RouteSnap.Drawing(pieces: [piece(.over65), piece(.to25), piece(nil), piece(.over65), piece(.to50)], routedGaps: [], gaps: [],
                                  start: nil, end: nil, hasSpeed: true)
        XCTAssertEqual(d.usedSpeeds, [.to25, .to50, .over65])
        let none = RouteSnap.Drawing(pieces: [piece(nil)], routedGaps: [], gaps: [], start: nil, end: nil, hasSpeed: false)
        XCTAssertEqual(none.usedSpeeds, [])
    }

    func testTextOverAMapHasOneSizeTheAppsCaptionSize() {
        // The speed chip, the credit, the legends and the pin labels all read `type.mapCaption`: a chip never reads smaller than the credit.
        XCTAssertEqual(ThemeTokens.typeMapCaption, 11)
        XCTAssertEqual(ThemeTokens.typeMapCaption, ThemeTokens.typeCaption)
    }
}
