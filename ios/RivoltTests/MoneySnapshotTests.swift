import SwiftUI
import XCTest
@testable import Rivolt

/// The Money tab drawn from the shared ledger fixtures, next to qa's web references (Money at 390 px, light and dark):
/// a past month with the off-peak estimate, and a month in progress with every price state the receipts can show.
@MainActor
final class MoneySnapshotTests: XCTestCase {
    private var cal: Calendar {
        var c = Calendar(identifier: .gregorian)
        c.timeZone = TimeZone(identifier: "America/Chicago")!
        return c
    }

    private let now = JSON.parseDate("2026-10-21T17:00:00Z")!

    private func json(_ name: String) throws -> [String: Any] {
        try XCTUnwrap(JSONSerialization.jsonObject(with: Fixture.data(name)) as? [String: Any])
    }

    private func decode<T: Decodable>(_ type: T.Type, _ obj: Any) throws -> T {
        try JSON.decoder().decode(T.self, from: JSONSerialization.data(withJSONObject: obj))
    }

    private func page(_ model: MoneyModel, title: String, filter: MoneyLogic.Filter = .all) -> some View {
        VStack(spacing: 0) {
            ThemeTopBar { EmptyView() }
            HStack {
                Text("Money").themeText(.hero)
                Spacer()
                ThemeStepper(title: title, canGoForward: false, previous: {}, next: {})
            }
            .padding(.horizontal, 16).padding(.vertical, 8)
            MoneyContent(model: model, filter: .constant(filter), metric: .constant(.cost), savingsWhich: .constant(.last),
                         noticeHidden: .constant(false), serverURL: URL(string: "https://rivolt.example"), now: now, calendar: cal,
                         usesMetric: false, export: .init(share: {}, csv: {}))
                .padding(16)
        }
        .frame(width: 390)
        .background(ThemePageBackground())
    }

    private func render(_ name: String, _ content: some View) throws {
        Theme.registerFonts()
        for scheme in [ColorScheme.light, .dark] {
            let renderer = ImageRenderer(content: content.environment(\.colorScheme, scheme).fixedSize(horizontal: false, vertical: true))
            renderer.scale = 3
            let image = try XCTUnwrap(renderer.uiImage, "\(name) did not render")
            XCTAssertGreaterThan(image.size.height, 600)
            let png = try XCTUnwrap(image.pngData())
            let file = "\(name)-\(scheme == .dark ? "dark" : "light").png"
            let attachment = XCTAttachment(data: png, uniformTypeIdentifier: "public.png")
            attachment.name = file
            attachment.lifetime = .keepAlways
            add(attachment)
            if let dir = ProcessInfo.processInfo.environment["SNAPSHOT_DIR"], !dir.isEmpty {
                try FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
                try png.write(to: URL(fileURLWithPath: dir).appendingPathComponent(file))
            }
        }
    }

    func testRenderAPastMonth() throws {
        let model = MoneyModel(month: try Fixture.decode(LedgerMonth.self, "ledger_monthly"),
                               summary: try Fixture.decode(LedgerSummary.self, "ledger_summary"),
                               savings: try Fixture.decode(LedgerSavings.self, "ledger_savings"))
        try render("money-past-month", page(model, title: "Sep 2026"))
    }

    /// Money for someone on a flat rate: no Off-peak savings card.
    func testRenderAFlatRateSavingsCard() throws {
        let model = MoneyModel(month: try Fixture.decode(LedgerMonth.self, "ledger_monthly"),
                               summary: try Fixture.decode(LedgerSummary.self, "ledger_summary"),
                               savings: try Fixture.decode(LedgerSavings.self, "ledger_savings_flat"))
        try render("money-flat-rate", page(model, title: "Sep 2026"))
    }

    /// October so far, compared with September 1–21, with an estimate, a likely wrong home rate, a pending fast charge,
    /// fragments, the observed-price notice, two charges to confirm and the off-peak card asking for a schedule.
    func testRenderAMonthInProgressWithEveryPriceState() throws {
        var m = try json("ledger_monthly")
        m["period"] = "2026-10"
        m["is_current_month"] = true
        m["range"] = ["start": "2026-10-01T05:00:00Z", "end": "2026-11-01T05:00:00Z"]
        m["first_activity_at"] = "2026-10-01T06:00:00Z"
        var spend = m["spend"] as! [String: Any]
        spend["observed_estimate_sessions"] = 1
        spend["observed_estimate_cost"] = 18.44
        spend["estimated_share"] = 0.37
        spend["estimate_pending_sessions"] = 1
        spend["estimate_pending_kwh"] = 37.9
        m["spend"] = spend
        var rs = m["receipts"] as! [[String: Any]]
        func move(_ r: inout [String: Any], day: String) {
            for k in ["started_at", "ended_at"] { r[k] = (r[k] as! String).replacingOccurrences(of: "2026-09-", with: "2026-10-") }
            r["local_date"] = "2026-10-\(day)"
            var ss = r["sessions"] as! [[String: Any]]
            for i in ss.indices { for k in ["started_at", "ended_at"] { ss[i][k] = (ss[i][k] as! String).replacingOccurrences(of: "2026-09-", with: "2026-10-") } }
            r["sessions"] = ss
        }
        for i in rs.indices { move(&rs[i], day: String((rs[i]["local_date"] as! String).suffix(2))) }
        // The away charge: two fragments, still unpriced.
        var frag = (rs[0]["sessions"] as! [[String: Any]])[0]
        frag["charge_id"] = "chg-away-1b"
        frag["kwh"] = 1.0
        rs[0]["sessions"] = (rs[0]["sessions"] as! [[String: Any]]) + [frag]
        rs[0]["session_count"] = 2
        rs[0]["flags"] = ["unpriced", "fragments_folded", "null_energy"]
        // The fast charge: estimated from the network, waiting for a look.
        rs[2]["cost_source"] = "network_estimate"
        rs[2]["network"] = "Electrify America"
        rs[2]["network_state"] = "estimated"
        rs[2]["cost"] = 18.44
        rs[2]["rate"] = 0.36
        var fs = (rs[2]["sessions"] as! [[String: Any]])[0]
        fs["cost_source"] = "network_estimate"
        fs["network_state"] = "estimated"
        rs[2]["sessions"] = [fs]
        // A home rate stamped away from home, and an old fast charge pending.
        rs[1]["place"] = "away"
        rs[1]["flags"] = ["home_rate_away"]
        var hs = (rs[1]["sessions"] as! [[String: Any]])[0]
        hs["flags"] = ["home_rate_away"]
        rs[1]["sessions"] = [hs]
        var pending = rs[2]
        pending["receipt_id"] = "rcpt_chg-fast-0"
        pending["cost_source"] = "home_rate_misapplied"
        pending["cost"] = NSNull()
        pending["network_state"] = NSNull()
        pending["flags"] = ["estimate_pending"]
        var ps = fs
        ps["charge_id"] = "chg-fast-0"
        ps["cost_source"] = "home_rate_misapplied"
        ps["cost"] = NSNull()
        ps["network_state"] = NSNull()
        pending["sessions"] = [ps]
        rs.append(pending)
        m["receipts"] = rs
        var sum = try json("ledger_summary")
        sum["to_confirm_count"] = 2
        let savings = try decode(LedgerSavings.self, ["needs": "offpeak_schedule", "currency": "USD"])
        let model = MoneyModel(month: try decode(LedgerMonth.self, m), summary: try decode(LedgerSummary.self, sum),
                               previous: try Fixture.decode(LedgerMonth.self, "ledger_monthly"), savings: savings)
        try render("money-month-to-date", page(model, title: "Oct 2026"))
    }
}
