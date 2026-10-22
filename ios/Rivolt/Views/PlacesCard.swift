import SwiftUI

/// "Charging locations": three tiles, Home, Away and Fast, each with the number of sessions, the energy added and its
/// share of the energy, for the period picked above the charts. Home and Away are AC charges by place and Fast is a DC
/// fast charge wherever it was (the same rules as the web). A list of counts: it never shows where.
struct PlacesCard: View {
    let split: ChargeSplit
    /// The period the figures cover, as the stepper reads it ("Oct 2026").
    let periodTitle: String
    @Environment(\.dynamicTypeSize) private var typeSize

    private struct Row: Identifiable {
        let id: String
        let bucket: ChargeSplit.Bucket
        let share: Int
    }

    private var rows: [Row] {
        let buckets = [("Home", split.home), ("Away", split.away), ("Fast", split.fast)]
        let shares = PlacesMath.shareByEnergy(buckets.map { $0.1.energyKwh })
        return zip(buckets, shares).map { Row(id: $0.0, bucket: $0.1, share: $1) }
    }

    var body: some View {
        ThemeSection(title: "Charging locations") {
            let layout = typeSize.isAccessibilitySize ? AnyLayout(VStackLayout(alignment: .leading, spacing: 10)) : AnyLayout(HStackLayout(alignment: .top, spacing: 12))
            layout {
                ForEach(rows) { r in tile(r) }
            }
            Text("\(periodTitle). Share of the energy added.").themeText(.caption)
        }
    }

    /// Home is the accent, Away is neutral and Fast is the warm tone, as on the web.
    private func tone(_ label: String) -> (ink: Color, edge: Color, fill: Color) {
        switch label {
        case "Home": return (Theme.accent, Theme.accentFill.opacity(0.4), Theme.accentFill.opacity(0.1))
        case "Fast": return (Theme.estimate, Theme.estimateFill.opacity(0.4), Theme.estimateFill.opacity(0.1))
        default: return (Theme.textBody, Theme.borderStrong, .clear)
        }
    }

    private func tile(_ r: Row) -> some View {
        let t = tone(r.id)
        return VStack(alignment: .leading, spacing: 6) {
            HStack {
                Text(r.id).themeText(.caption, color: t.ink)
                    .lineLimit(1).fixedSize()
                    .padding(.horizontal, 2 * Theme.unit).padding(.vertical, Theme.unit / 2)
                    .background(t.fill, in: Capsule())
                    .overlay(Capsule().strokeBorder(t.edge, lineWidth: 1))
                Spacer(minLength: 4)
                Text("\(r.share)%").themeText(.caption).lineLimit(1).minimumScaleFactor(0.8)
            }
            HStack(alignment: .firstTextBaseline, spacing: 4) {
                Text("\(r.bucket.sessions)").themeText(.title).lineLimit(1).fixedSize()
                Text(r.bucket.sessions == 1 ? "session" : "sessions").themeText(.caption).lineLimit(1).minimumScaleFactor(0.7)
            }
            Text("\(NowFormat.oneDecimal(r.bucket.energyKwh)) kWh added").themeText(.caption)
                .fixedSize(horizontal: false, vertical: true)
        }
        .padding(3 * Theme.unit)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Theme.card.opacity(0.4), in: RoundedRectangle(cornerRadius: Theme.radiusCard, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: Theme.radiusCard, style: .continuous).strokeBorder(Theme.border, lineWidth: 1))
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("\(r.id), \(r.bucket.sessions) \(r.bucket.sessions == 1 ? "session" : "sessions"), \(NowFormat.oneDecimal(r.bucket.energyKwh)) kilowatt hours added, \(r.share) percent of the energy")
    }
}
