import SwiftUI

/// The "Your week" strip at the top of Now: this week's miles, energy added and charging cost, and the miles against last week so far.
/// Cost says "est." unless every priced charge was stated by Rivian or the owner. Labelled as the sample truck's week in the demo.
struct YourWeekStrip: View {
    let week: YourWeekLogic.Week
    var demo = false

    private var f: YourWeekLogic.Figures { week.figures }
    private var costText: String? {
        f.cost > 0 ? "\(NumberText.money(f.cost, f.currency))\(f.costEstimated ? " est." : "") charging" : nil
    }
    private var spoken: String {
        var parts = ["Your week", "\(Int(f.miles.rounded())) miles", "\(Int(f.kWh.rounded())) kilowatt hours added"]
        if let c = costText { parts.append(c) }
        if let ch = YourWeekLogic.changeLine(week.milesChangePct) { parts.append(ch) }
        if demo { parts.append(YourWeekLogic.sampleLabel) }
        return parts.joined(separator: ", ")
    }

    private func big(_ n: String, _ unit: String) -> Text {
        Text(n).font(Theme.sans(ThemeTokens.typeTitle, .semibold, relativeTo: .title)).foregroundStyle(Theme.textPrimary)
            + Text(unit).font(Theme.sans(ThemeTokens.typeSubline, .regular)).foregroundStyle(Theme.textSecondary)
    }

    @ViewBuilder
    private var figures: some View {
        big("\(Int(f.miles.rounded()))", " mi")
        big("\(Int(f.kWh.rounded()))", " kWh added")
        if let c = costText { Text(c).themeText(.value, color: Theme.textSecondary) }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 4) {
                Text(YourWeekLogic.title).themeText(.label)
                if demo { Text("· \(YourWeekLogic.sampleLabel)").themeText(.label, color: Theme.textTertiary) }
            }
            ViewThatFits(in: .horizontal) {
                HStack(alignment: .lastTextBaseline, spacing: 16) { figures }
                VStack(alignment: .leading, spacing: 2) { figures }
            }
            if let ch = YourWeekLogic.changeLine(week.milesChangePct) {
                Text(ch).themeText(.subline, color: Theme.textTertiary)
            }
        }
        .padding(3 * Theme.unit)
        .frame(maxWidth: .infinity, alignment: .leading)
        .themeCard(padding: 0)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(spoken)
    }
}
