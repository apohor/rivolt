import XCTest
@testable import Rivolt

final class NotificationRouteTests: XCTestCase {
    func testEveryTargetTheServerStoresHasAScreen() {
        XCTAssertEqual(NotificationRoute.parse("/"), .home)
        XCTAssertEqual(NotificationRoute.parse("/money"), .money)
        XCTAssertEqual(NotificationRoute.parse("/charges/3f2b8c1e-5d4a-4e6f-9a7b-0c1d2e3f4a5b"), .charge("3f2b8c1e-5d4a-4e6f-9a7b-0c1d2e3f4a5b"))
        XCTAssertEqual(NotificationRoute.parse("/plan?trip=3f2b8c1e-5d4a-4e6f-9a7b-0c1d2e3f4a5b&tab=pva"), .trip("3f2b8c1e-5d4a-4e6f-9a7b-0c1d2e3f4a5b"))
        XCTAssertEqual(NotificationRoute.parse("/plan?tab=pva&trip=abc-123"), .trip("abc-123"), "the order of the query does not matter")
    }

    func testAnythingUnknownOrOddIsHome() {
        for bad in [nil, "", "/settings", "/plan", "/plan?tab=pva", "/plan?trip=", "/plan?trip=not an id", "/plan?trip=../x",
                    "/charges/", "/charges/../../etc", "/charges/zz", "//evil.example/money", "https://evil.example/money", "money",
                    "/money/extra", "/" + String(repeating: "a", count: 300)] as [String?] {
            XCTAssertEqual(NotificationRoute.parse(bad), .home, bad ?? "nil")
        }
    }

    func testEachRouteShowsItsTabAndNowWhereTheTabIsNotOffered() {
        let all = ["now", "activity", "money", "plan", "truck"]
        XCTAssertEqual(NotificationRoute.home.tab(available: all), "now")
        XCTAssertEqual(NotificationRoute.money.tab(available: all), "money")
        XCTAssertEqual(NotificationRoute.charge("a1").tab(available: all), "activity")
        XCTAssertEqual(NotificationRoute.trip("a1").tab(available: all), "plan")
        // A server without the planner has no Plan tab.
        XCTAssertEqual(NotificationRoute.trip("a1").tab(available: ["now", "activity", "money", "truck"]), "now")
    }

    func testTheTapTargetIsReadFromTheStoredNotification() {
        let json = #"{"id":"a1","kind":"trip_recap","title":"You drove a saved trip","body":"x","url":"/plan?trip=a1&tab=pva","created_at":"2026-10-28T10:00:00Z"}"#
        XCTAssertEqual(NotificationFetch.tapURL(Data(json.utf8)), "/plan?trip=a1&tab=pva")
        XCTAssertEqual(NotificationRoute.parse(NotificationFetch.tapURL(Data(json.utf8))), .trip("a1"))
        XCTAssertNil(NotificationFetch.tapURL(Data(#"{"title":"t","body":"b"}"#.utf8)))
        XCTAssertNil(NotificationFetch.tapURL(Data("not json".utf8)))
    }

    func testTheTextTheExtensionReadsIsUnchanged() {
        let json = #"{"title":" Charge complete ","body":"80%","url":"/charges/a1"}"#
        XCTAssertEqual(NotificationFetch.decode(Data(json.utf8)), NotificationFetch.Text(title: "Charge complete", body: "80%"))
    }
}
