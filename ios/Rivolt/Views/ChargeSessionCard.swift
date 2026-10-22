import SwiftUI

/// One labelled value on the session card.
struct InsightRow: Identifiable, Equatable {
    let label: String
    let value: String
    var hint: String? = nil
    /// What VoiceOver says for the value when the printed one has symbols it would read badly.
    var spoken: String? = nil
    var id: String { label }
}

/// The wording and units of the session card's rows, apart from the view so a test can read them.
enum ChargeSessionRows {
    struct Group: Equatable {
        let title: String
        let rows: [InsightRow]
    }

    static func groups(_ c: ActivityCharge, _ i: ChargeInsights, metric: Bool = NowFormat.usesMetric) -> [Group] {
        func f(_ v: Double, _ d: Int) -> String { NumberText.fixed(v, d) }
        var groups: [Group] = []

        let place = ActivityLogic.placeLabel(c)
        let where_ = place == "Fast" ? "Fast charge" : place ?? "—"
        groups.append(Group(title: "Session", rows: [
            InsightRow(label: "Where", value: where_),
            InsightRow(label: "Charging tier", value: i.tier.label, hint: i.tier.label == "—" ? nil : i.tier.hint),
        ]))

        let peak = c.maxPowerKw > 0 ? "\(f(c.maxPowerKw, 1)) kW" : "—"
        let toPeak = i.timeToPeakSec > 0 ? ActivityLogic.shortDuration(i.timeToPeakSec) : "—"
        let active = i.activeSec > 0 ? "\(ActivityLogic.shortDuration(i.activeSec)) (\(f(i.activePct, 0))%)" : "—"
        groups.append(Group(title: "Power & timing", rows: [
            InsightRow(label: "Peak power", value: peak),
            InsightRow(label: "Time to peak", value: toPeak),
            InsightRow(label: "Active charging", value: active, spoken: i.activeSec > 0 ? "\(spokenDuration(i.activeSec)), \(f(i.activePct, 0)) percent of the session" : nil),
        ]))

        var gained = f(i.socGainedPct, 1) + "%"
        if c.startSocPct > 0, c.endSocPct > 0 { gained += " (\(Int(c.startSocPct.rounded()))% → \(Int(c.endSocPct.rounded()))%)" }
        let rate = i.socPerHour > 0 ? "\(f(i.socPerHour, 1)) %/h" : "—"
        groups.append(Group(title: "Battery & energy", rows: [
            InsightRow(label: "SoC gained", value: gained),
            InsightRow(label: "Charge rate", value: rate, spoken: i.socPerHour > 0 ? "\(f(i.socPerHour, 1)) percent per hour" : nil),
            InsightRow(label: "Charge limit", value: i.limitPct.map { "\($0)%" } ?? "—"),
            InsightRow(label: "Energy added", value: "\(f(c.energyAddedKwh, 2)) kWh"),
            InsightRow(label: "Price per kWh", value: i.costPerKwh ?? "—", spoken: i.costPerKwh.map { SpokenText.words($0).replacingOccurrences(of: "/kWh", with: " per kilowatt hour") }),
        ]))

        if i.outsideTempC != nil || i.insideTempC != nil {
            func t(_ c: Double?) -> String {
                guard let c else { return "—" }
                return "\(f(metric ? c : c * 1.8 + 32, 0))\(metric ? "°C" : "°F")"
            }
            groups.append(Group(title: "Environment", rows: [
                InsightRow(label: "Outside temperature", value: t(i.outsideTempC)),
                InsightRow(label: "Inside temperature", value: t(i.insideTempC)),
            ]))
        }
        return groups
    }

    private static func spokenDuration(_ seconds: TimeInterval) -> String {
        ChartMath.spokenMinutes(seconds / 60)
    }
}

/// The web's "Session" card: what happened in the charge, in thematic groups. The rows that need the sample stream
/// show "—" until it arrives (or if it can't be read); the rest come from the charge's own row.
struct ChargeSessionCard: View {
    let charge: ActivityCharge
    let insights: ChargeInsights

    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        ThemeSection(title: "Session") {
            VStack(alignment: .leading, spacing: 16) {
                ForEach(ChargeSessionRows.groups(charge, insights), id: \.title) { group in
                    VStack(alignment: .leading, spacing: 8) {
                        // The card already says "Session"; its first group has no heading of its own.
                        if group.title != "Session" { Text(group.title).themeText(.label).accessibilityAddTraits(.isHeader) }
                        let columns = typeSize.isAccessibilitySize
                            ? [GridItem(.flexible())]
                            : [GridItem(.flexible(), spacing: 12, alignment: .topLeading), GridItem(.flexible(), spacing: 12, alignment: .topLeading)]
                        LazyVGrid(columns: columns, alignment: .leading, spacing: 12) {
                            ForEach(group.rows) { row in
                                VStack(alignment: .leading, spacing: 2) {
                                    Text(row.label).themeText(.label)
                                    Text(row.value).themeText(.value, color: Theme.textPrimary)
                                    if let hint = row.hint { Text(hint).themeText(.subline, color: Theme.textTertiary) }
                                }
                                .frame(maxWidth: .infinity, alignment: .leading)
                                .accessibilityElement(children: .ignore)
                                .accessibilityLabel(row.label)
                                .accessibilityValue(SpokenText.words(row.spoken ?? row.value) + (row.hint.map { ", \(SpokenText.words($0))" } ?? ""))
                            }
                        }
                    }
                }
            }
            .padding(4 * Theme.unit)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
    }
}
