import SwiftUI

/// The charge in progress: power, the climb to the charge limit, the time left and what it has cost so far. Read-only,
/// the same figures as the web's Now page, with our own cost always marked as an estimate.
struct ChargingDetailCard: View {
    let detail: ChargingDetail
    @Environment(\.dynamicTypeSize) private var typeSize
    @ScaledMetric(relativeTo: .largeTitle) private var bigNumber: CGFloat = 34

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            // At accessibility sizes the title, tag and limit stack instead of squeezing side by side.
            let layout = typeSize.isAccessibilitySize ? AnyLayout(VStackLayout(alignment: .leading, spacing: 6)) : AnyLayout(HStackLayout(alignment: .firstTextBaseline))
            layout {
                Label("Charging session", systemImage: "bolt.fill")
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(NowInk.charging)
                if !typeSize.isAccessibilitySize { Spacer() }
                if let place = detail.place {
                    Text(place).font(.caption.weight(.medium)).foregroundStyle(NowInk.secondary)
                        .padding(.horizontal, 8).padding(.vertical, 2)
                        .background(.quaternary, in: Capsule())
                }
            }
            layout {
                HStack(alignment: .firstTextBaseline, spacing: 6) {
                    Text(detail.powerKw.map(NowFormat.oneDecimal) ?? "—")
                        .font(.system(size: bigNumber, weight: .semibold, design: .rounded)).monospacedDigit()
                    Text("kW").font(.subheadline).foregroundStyle(NowInk.secondary)
                }
                if !typeSize.isAccessibilitySize { Spacer() }
                if let to = detail.toTargetPct, let target = detail.targetPct {
                    Text("\(Int(to.rounded())) \(Int(to.rounded()) == 1 ? "point" : "points") to \(Int(target.rounded()))% limit").font(.subheadline).foregroundStyle(NowInk.secondary)
                }
            }
            if detail.haveLiveData {
                FieldGrid {
                    if let r = detail.remaining { Field(label: "Time left", value: "about \(NowFormat.duration(r))") }
                    if let e = detail.elapsed { Field(label: "Elapsed", value: NowFormat.duration(e)) }
                    if let kwh = detail.energyKwh { Field(label: "Energy added", value: "\(NowFormat.oneDecimal(kwh)) kWh") }
                    if let km = detail.rangeAddedKm { Field(label: "Range added", value: distance(km: km)) }
                    if let rate = detail.rateKmPerHour { Field(label: "Rate", value: "\(distance(km: rate))/h") }
                    if let cost = detail.cost { Field(label: "Cost so far", value: NowFormat.costText(cost)) }
                }
            } else {
                Text("Waiting for the first reading from the charger.").font(.footnote).foregroundStyle(NowInk.secondary)
            }
        }
        .padding(20)
        .background(.background, in: RoundedRectangle(cornerRadius: 20))
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(detail.spoken)
    }

    private func distance(km: Double) -> String {
        Measurement(value: km, unit: UnitLength.kilometers)
            .formatted(.measurement(width: .abbreviated, usage: .road, numberFormatStyle: .number.precision(.fractionLength(0))))
    }
}

/// The drive in progress, or the last one.
struct DriveDetailCard: View {
    let detail: DriveDetail
    var clock: Date = .now
    @ScaledMetric(relativeTo: .title) private var bigNumber: CGFloat = 30

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Label(title, systemImage: "steeringwheel").font(.subheadline.weight(.semibold))
            Text(NowFormat.distance(mi: detail.distanceMi)).font(.system(size: bigNumber, weight: .semibold, design: .rounded)).monospacedDigit()
            FieldGrid {
                if let e = detail.elapsed { Field(label: "Time", value: NowFormat.duration(e)) }
                if let v = detail.avgSpeedMph { Field(label: "Average speed", value: NowFormat.speed(mph: v)) }
                if let kwh = detail.energyKwh { Field(label: "Energy used", value: "\(NowFormat.oneDecimal(kwh)) kWh") }
                if let eff = detail.miPerKwh { Field(label: "Efficiency", value: NowFormat.efficiency(miPerKwh: eff)) }
                if let c = detail.estimatedCost, let m = NowFormat.moneyIfKnown(c, currency: detail.currency) {
                    Field(label: "Cost", value: "~\(m) (estimate)")
                }
            }
        }
        .padding(20)
        .background(.background, in: RoundedRectangle(cornerRadius: 20))
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(detail.spoken(title: title))
    }

    private var title: String {
        switch detail.kind {
        case .now: return "Driving now"
        case .last(let ended):
            // A clock a little behind the server's must not read "in 5 minutes".
            guard let ended, ended <= clock else { return "Last drive" }
            let f = RelativeDateTimeFormatter()
            f.unitsStyle = .full
            return "Last drive, \(f.localizedString(for: ended, relativeTo: clock))"
        }
    }

}

/// Two columns, or one at accessibility text sizes where half a card is too narrow for "about 1 h 12 min".
private struct FieldGrid<Content: View>: View {
    @Environment(\.dynamicTypeSize) private var typeSize
    @ViewBuilder var content: Content

    var body: some View {
        let columns = typeSize.isAccessibilitySize
            ? [GridItem(.flexible(), alignment: .leading)]
            : [GridItem(.flexible(), alignment: .leading), GridItem(.flexible(), alignment: .leading)]
        LazyVGrid(columns: columns, alignment: .leading, spacing: 10) { content }
    }
}

private struct Field: View {
    let label: String
    let value: String

    var body: some View {
        VStack(alignment: .leading, spacing: 1) {
            Text(label).font(.caption).foregroundStyle(NowInk.secondary)
            Text(value).font(.subheadline.weight(.medium)).monospacedDigit()
        }
    }
}
