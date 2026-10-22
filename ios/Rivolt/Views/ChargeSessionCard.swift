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

        // The kind of charge (the web's "Where"); the network, when the server matched one, under it.
        let place = ActivityLogic.placeLabel(c)
        let type = place == "Fast" ? "Fast charge" : place ?? "—"
        let network = c.network.flatMap { $0.isEmpty ? nil : $0 }
        var session: [InsightRow] = [
            InsightRow(label: "Type", value: type, hint: network),
            InsightRow(label: "Charging tier", value: i.tier.label, hint: i.tier.label == "—" ? nil : i.tier.hint),
        ]
        if let state = i.finalState { session.append(InsightRow(label: "Final state", value: state)) }
        if let source = i.source { session.append(InsightRow(label: "Source", value: source)) }
        groups.append(Group(title: "Session", rows: session))

        let peak = c.maxPowerKw > 0 ? "\(f(c.maxPowerKw, 1)) kW" : "—"
        let toPeak = i.timeToPeakSec > 0 ? ActivityLogic.shortDuration(i.timeToPeakSec) : "—"
        let active = i.activeSec > 0 ? "\(ActivityLogic.shortDuration(i.activeSec)) (\(f(i.activePct, 0))%)" : "—"
        var power: [InsightRow] = [InsightRow(label: "Peak power", value: peak)]
        if let avg = i.avgPowerKw { power.append(InsightRow(label: "Average power", value: "\(f(avg, 1)) kW")) }
        power.append(InsightRow(label: "Time to peak", value: toPeak))
        power.append(InsightRow(label: "Active charging", value: active, spoken: i.activeSec > 0 ? "\(spokenDuration(i.activeSec)), \(f(i.activePct, 0)) percent of the session" : nil))
        groups.append(Group(title: "Power & timing", rows: power))

        var gained = f(i.socGainedPct, 1) + "%"
        if c.startSocPct > 0, c.endSocPct > 0 { gained += " (\(Int(c.startSocPct.rounded()))% → \(Int(c.endSocPct.rounded()))%)" }
        let rate = i.socPerHour > 0 ? "\(f(i.socPerHour, 1)) %/h" : "—"
        var battery: [InsightRow] = [
            InsightRow(label: "SoC gained", value: gained),
            InsightRow(label: "Charge rate", value: rate, spoken: i.socPerHour > 0 ? "\(f(i.socPerHour, 1)) percent per hour" : nil),
            InsightRow(label: "Charge limit", value: i.limitPct.map { "\($0)%" } ?? "—"),
            InsightRow(label: "Energy added", value: "\(f(c.energyAddedKwh, 2)) kWh"),
        ]
        if let miles = i.milesAdded {
            battery.append(InsightRow(label: "Range added", value: NowFormat.distance(mi: miles), spoken: NowFormat.distance(mi: miles, spoken: true)))
        }
        if let eff = i.miPerKwh { battery.append(InsightRow(label: "Range efficiency", value: NowFormat.efficiency(miPerKwh: eff), spoken: NowFormat.efficiency(miPerKwh: eff, spoken: true))) }
        if let perMile = i.costPerMile {
            battery.append(InsightRow(label: "Price per \(metric ? "kilometre" : "mile")", value: perMile,
                                      spoken: SpokenText.words(perMile).replacingOccurrences(of: metric ? "/km" : "/mi", with: metric ? " per kilometre" : " per mile")))
        }
        groups.append(Group(title: "Battery & energy", rows: battery))

        // The battery management system's own energy (a Parallax figure; absent on older rows and other sources).
        if let thermal = i.thermalKwh {
            groups.append(Group(title: "Thermal management", rows: [
                InsightRow(label: "BMS energy", value: "\(f(thermal, 2)) kWh"),
                InsightRow(label: "Share of total", value: i.thermalSharePct.map { "\(f($0, 1))%" } ?? "—"),
                InsightRow(label: "Conditioning intensity", value: ChargeInsights.thermalIntensity(i.thermalSharePct)),
            ]))
        }

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

    /// What the thermal group means, under it.
    static let thermalNote = "Energy the battery management system spent heating or cooling the pack during this charge. A high share means the pack was being conditioned hard: a cold-soaked fast charge, a hot day, or preconditioning before departure."

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
                ForEach(Array(ChargeSessionRows.groups(charge, insights).enumerated()), id: \.offset) { index, group in
                    VStack(alignment: .leading, spacing: 10) {
                        // The card already says "Session"; its first group has no heading of its own. The others read as
                        // groups: a hairline above and a heavier heading in the body colour, not the fields' small grey caps.
                        if index > 0 {
                            Rectangle().fill(Theme.border).frame(height: 1).accessibilityHidden(true)
                            Text(group.title).themeText(.value, color: Theme.textBody).fontWeight(.semibold).accessibilityAddTraits(.isHeader)
                        }
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
                        if group.title == "Thermal management" {
                            Text(ChargeSessionRows.thermalNote).themeText(.caption, color: Theme.textTertiary)
                        }
                    }
                }
            }
            .padding(4 * Theme.unit)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
    }
}
