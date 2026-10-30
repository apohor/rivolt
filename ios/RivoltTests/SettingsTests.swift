import XCTest
@testable import Rivolt

final class SettingsTests: XCTestCase {
    private func shape(_ offered: Bool) -> [[String]] {
        SettingsLogic.groups(notificationsOffered: offered).map { [$0.title] + $0.rows.map(\.title) }
    }

    func testTheSectionsAndRowsAreTheWebsSettingsInOrder() {
        XCTAssertEqual(shape(true), [
            ["Account", "Connection details", "Sign out"],
            ["Security", "Signed-in devices", "Passkeys"],
            ["Vehicle", "Vehicle profile", "Home location", "Units"],
            ["Charging", "Charging rates", "Off-peak schedule", "Charging networks", "Trip planner defaults"],
            ["Notifications", "Notifications"],
            ["App", "Appearance"],
            ["Data", "Export your data", "Delete account"],
            ["About", "Privacy policy", "Terms", "Support", "Acknowledgements"],
        ])
    }

    func testNotificationsIsLeftOutWhereTheServerCannotPush() {
        let titles = SettingsLogic.groups(notificationsOffered: false).map(\.title)
        XCTAssertEqual(titles, ["Account", "Security", "Vehicle", "Charging", "App", "Data", "About"])
    }

    func testDeleteAccountIsTheLastRowOfData() {
        XCTAssertEqual(SettingsLogic.groups(notificationsOffered: true).first { $0.title == "Data" }?.rows.last, .deleteAccount)
    }

    func testTheVersionLineNamesTheVersionAndTheBuild() {
        let line = SettingsLogic.versionLine()
        XCTAssertTrue(line.hasPrefix("Version "), line)
        XCTAssertTrue(line.contains("("), line)
    }

    func testEveryRowHasAnIconAndATitleOfItsOwn() {
        let rows = SettingsLogic.groups(notificationsOffered: true).flatMap(\.rows)
        XCTAssertEqual(Set(rows.map(\.title)).count, rows.count)
        XCTAssertTrue(rows.allSatisfy { !$0.symbol.isEmpty })
    }
}

extension SettingsTests {
    func testSupportOpensTheWebsSupportPage() {
        let rows = SettingsLogic.groups(notificationsOffered: true).first { $0.title == "About" }?.rows
        XCTAssertEqual(rows, [.privacy, .terms, .support, .acknowledgements])
        XCTAssertEqual(SettingsLogic.Row.support.title, "Support")
        XCTAssertFalse(SettingsLogic.Row.support.symbol.isEmpty)
    }
}
