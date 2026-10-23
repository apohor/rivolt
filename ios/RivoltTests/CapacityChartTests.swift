import SwiftUI
import XCTest
@testable import Rivolt

/// The capacity chart's rules (the web's capacityChart.ts) and renders of the redesign for the owner's review, from a
/// synthetic history shaped like a real one: a January cluster, one May charge, a September–October cluster.
/// trends_capacity_spread.json carries energy_kwh and soc_delta_pct, which the server doesn't send yet (it isn't in
/// the server's strict fixture test until it does).
@MainActor
final class CapacityChartTests: XCTestCase {
    private func spread() throws -> TruckTrends { try Fixture.decode(TruckTrends.self, "trends_capacity_spread") }

    private func sample(_ d: String, _ k: Double, _ e: Double? = nil) -> TruckTrends.Capacity.Sample {
        TruckTrends.Capacity.Sample(at: JSON.parseDate("\(d)T08:00:00Z")!, kwhEffective: k, energyKwh: e)
    }

    func testTheWebsRules() throws {
        XCTAssertEqual(CapacityMath.strength(90, median: 138), 1)
        XCTAssertEqual(CapacityMath.strength(15, median: 138), 0.3)
        XCTAssertEqual(CapacityMath.strength(41.4, median: 138), 0.5, accuracy: 1e-9)
        XCTAssertEqual(CapacityMath.strength(nil, median: 138), 0.6)
        XCTAssertEqual(CapacityMath.yDomain(median: 138.4), 117...160)
        let d = CapacityMath.dots([sample("2026-01-03", 100), sample("2026-01-04", 140)], median: 138.4)
        XCTAssertEqual(d.map(\.kwh), [117, 140])
        XCTAssertEqual(d.map(\.outside), [true, false])
        XCTAssertTrue(CapacityMath.specInRange(141, median: 138.4))
        XCTAssertFalse(CapacityMath.specInRange(180, median: 138.4))
        let jan = ["03", "06", "09", "13", "16"].enumerated().map { sample("2026-01-\($1)", 136 + Double($0)) }
        let oct = ["01", "05", "09", "13"].enumerated().map { sample("2026-10-\($1)", 138 + Double($0)) }
        let runs = CapacityMath.trendRuns(jan + [sample("2026-05-14", 127)] + oct)
        XCTAssertEqual(runs.map(\.count), [5, 4])
        XCTAssertTrue(CapacityMath.trendRuns([sample("2026-01-01", 130), sample("2026-02-15", 140)]).isEmpty)
    }

    /// The server's trends (backend's fixture, #949) carry what each charge added: it weights the dots.
    func testTheServersSamplesCarryTheEnergyAdded() throws {
        let t = try Fixture.decode(TruckTrends.self, "trends")
        let first = try XCTUnwrap(t.capacity?.samples.first)
        XCTAssertEqual(first.energyKwh, 66.8)
        XCTAssertEqual(first.socDeltaPct, 55)
        let dots = CapacityMath.dots(t.capacity?.samples ?? [], median: t.capacity?.medianKwh ?? 0)
        XCTAssertTrue(dots.allSatisfy { $0.energyKwh != nil }, "every dot is weighted, none at the unknown 0.6")
    }

    func testTheSpreadHistory() throws {
        let c = try XCTUnwrap(try spread().capacity)
        XCTAssertEqual(c.samples.count, 20)
        XCTAssertEqual(c.samples[0].energyKwh, 72)
        XCTAssertEqual(CapacityMath.trendRuns(c.samples).count, 2, "January and September–October; nothing across the empty months")
    }

    func testRenderTheRedesign() throws {
        Theme.registerFonts()
        let t = try spread()
        let c = try XCTUnwrap(t.capacity)
        let view = VStack(alignment: .leading, spacing: 6) {
            Text("Battery capacity").themeText(.value, color: Theme.textPrimary).fontWeight(.medium)
            (Text("About \(TruckLogic.fixed(c.medianKwh ?? 0, 0)) kWh ").font(Theme.sans(ThemeTokens.typeTitle, .semibold, relativeTo: .title)).foregroundStyle(Theme.textPrimary)
                + Text("usable (estimate)").font(Theme.sans(ThemeTokens.typeValue, .regular, relativeTo: .body)).foregroundStyle(Theme.textSecondary))
            Text("from \(c.samples.count) charges").themeText(.subline, color: Theme.textTertiary)
            CapacityChart(samples: c.samples, median: c.medianKwh ?? 0, spec: c.specKwh ?? 0).frame(height: 220)
        }
        .padding(16)
        .frame(width: 390, alignment: .leading)
        .themePage()
        for scheme in [ColorScheme.light, .dark] {
            let renderer = ImageRenderer(content: view.environment(\.colorScheme, scheme).fixedSize(horizontal: false, vertical: true))
            renderer.scale = 3
            let image = try XCTUnwrap(renderer.uiImage)
            let png = try XCTUnwrap(image.pngData())
            let name = "capacity-dots-\(scheme == .dark ? "dark" : "light").png"
            let attachment = XCTAttachment(data: png, uniformTypeIdentifier: "public.png")
            attachment.name = name
            attachment.lifetime = .keepAlways
            add(attachment)
            if let dir = ProcessInfo.processInfo.environment["SNAPSHOT_DIR"], !dir.isEmpty {
                try FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
                try png.write(to: URL(fileURLWithPath: dir).appendingPathComponent(name))
            }
        }
    }
}
