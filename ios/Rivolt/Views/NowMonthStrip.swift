import SwiftUI

/// The month under the hero (design_preview now_v2): four figures in a 2x2 block, as the web's strip: distance, energy added,
/// efficiency and what charging cost. The period stepper sits beside its title.
struct NowMonthStripFigure: Equatable, Identifiable {
    var id: String { label }
    let value: String
    let label: String

    /// The four, from the same tiles the six-tile grid shows (so the figures are the same ones). A tile that is missing reads as a dash.
    static func make(tiles: [KpiTile], metric: Bool) -> [NowMonthStripFigure] {
        func tile(_ id: String) -> KpiTile? { tiles.first { $0.id == id } }
        /// Whole numbers for distance and energy, as the web's strip (1052, not 1051.6); a dash or other text stays as it is.
        func whole(_ id: String) -> String {
            guard let v = tile(id)?.value else { return "—" }
            guard let n = Double(v.replacingOccurrences(of: ",", with: ".")) else { return v }
            return NumberText.fixed(n, 0)
        }
        return [
            NowMonthStripFigure(value: whole("miles"), label: metric ? "km" : "miles"),
            NowMonthStripFigure(value: whole("energy"), label: "kWh added"),
            NowMonthStripFigure(value: tile("efficiency")?.value ?? "—", label: metric ? "km/kWh" : "mi/kWh"),
            NowMonthStripFigure(value: tile("cost")?.value ?? "—", label: "spent"),
        ]
    }
}

struct NowMonthStrip: View {
    let figures: [NowMonthStripFigure]
    @Environment(AppState.self) private var state
    @Environment(\.dynamicTypeSize) private var typeSize

    private var title: String {
        let p = state.period, cal = Calendar.current
        return p.kind == .month && p.isCurrent(now: .now, calendar: cal) ? "This month" : p.title(calendar: cal)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            ViewThatFits(in: .horizontal) {
                HStack { heading; Spacer(minLength: 8); PeriodControl() }
                VStack(alignment: .leading, spacing: 8) { heading; PeriodControl() }
            }
            .padding(.top, 8)
            // One column at accessibility text sizes, where two would clip the figures.
            let columns = typeSize.isAccessibilitySize ? 1 : 2
            VStack(spacing: 1) {
                ForEach(Array(stride(from: 0, to: figures.count, by: columns)), id: \.self) { start in
                    HStack(spacing: 1) {
                        ForEach(figures[start..<min(start + columns, figures.count)]) { f in cell(f) }
                    }
                }
            }
            .background(Theme.border)
            .clipShape(RoundedRectangle(cornerRadius: Theme.radiusCard, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: Theme.radiusCard, style: .continuous).strokeBorder(Theme.border, lineWidth: 1))
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("now-month")
    }

    private var heading: some View {
        Text(title).themeText(.value).fontWeight(.semibold).accessibilityAddTraits(.isHeader)
    }

    private func cell(_ f: NowMonthStripFigure) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(f.value).themeText(.title).lineLimit(1).minimumScaleFactor(0.6)
            Text(f.label).themeText(.value, color: Theme.textSecondary).lineLimit(1)
        }
        .padding(.horizontal, 12).padding(.vertical, 12)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Theme.card)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(SpokenText.words("\(f.value) \(f.label)"))
    }
}
