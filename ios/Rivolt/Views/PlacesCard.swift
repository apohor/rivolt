import SwiftUI

/// "Charging locations": three tiles, Home, Away and Fast, each with the number of sessions, the energy added and its
/// share of the energy, for the period picked above the charts. Home and Away are AC charges by place and Fast is a DC
/// fast charge wherever it was (the same rules as the web). A list of counts: it never shows where.
struct PlacesCard: View {
    let split: ChargeSplit
    let days: Int
    @Environment(\.dynamicTypeSize) private var typeSize

    private struct Row: Identifiable {
        let id: String
        let tint: Color
        let bucket: ChargeSplit.Bucket
        let share: Int
    }

    private var rows: [Row] {
        let buckets = [("Home", NowInk.charging, split.home), ("Away", NowInk.secondary, split.away), ("Fast", NowInk.stale, split.fast)]
        let shares = PlacesMath.shareByEnergy(buckets.map { $0.2.energyKwh })
        return zip(buckets, shares).map { Row(id: $0.0, tint: $0.1, bucket: $0.2, share: $1) }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Charging locations").font(.subheadline.weight(.semibold)).accessibilityAddTraits(.isHeader)
            let layout = typeSize.isAccessibilitySize ? AnyLayout(VStackLayout(alignment: .leading, spacing: 10)) : AnyLayout(HStackLayout(alignment: .top, spacing: 10))
            layout {
                ForEach(rows) { r in tile(r) }
            }
            Text("Last \(days) days. Share of the energy added.").font(.caption).foregroundStyle(NowInk.secondary)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(20)
        .background(.background, in: RoundedRectangle(cornerRadius: 20))
    }

    private func tile(_ r: Row) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack {
                Text(r.id).font(.caption.weight(.semibold)).foregroundStyle(r.tint)
                Spacer(minLength: 4)
                Text("\(r.share)%").font(.caption).foregroundStyle(NowInk.secondary).monospacedDigit()
            }
            Text("\(r.bucket.sessions)").font(.title2.weight(.semibold)).monospacedDigit()
            + Text(" \(r.bucket.sessions == 1 ? "session" : "sessions")").font(.caption).foregroundColor(NowInk.secondary)
            Text("\(NowFormat.oneDecimal(r.bucket.energyKwh)) kWh added").font(.caption2).foregroundStyle(NowInk.secondary).fixedSize(horizontal: false, vertical: true)
        }
        .padding(10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .overlay(RoundedRectangle(cornerRadius: 12).stroke(.quaternary))
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("\(r.id), \(r.bucket.sessions) \(r.bucket.sessions == 1 ? "session" : "sessions"), \(NowFormat.oneDecimal(r.bucket.energyKwh)) kilowatt hours added, \(r.share) percent of the energy")
    }
}
