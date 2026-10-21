import SwiftUI

/// The charge in progress: power, the climb to the charge limit, the time left and what it has cost so far. Read-only,
/// the same figures as the web's Now page, with our own cost always marked as an estimate.
struct ChargingDetailCard: View {
    let detail: ChargingDetail

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(alignment: .firstTextBaseline) {
                Label("Charging session", systemImage: "bolt.fill")
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(NowInk.charging)
                Spacer()
                if let place = detail.place {
                    Text(place).font(.caption.weight(.medium)).foregroundStyle(NowInk.secondary)
                        .padding(.horizontal, 8).padding(.vertical, 2)
                        .background(.quaternary, in: Capsule())
                }
            }
            HStack(alignment: .firstTextBaseline, spacing: 6) {
                Text(String(format: "%.1f", detail.powerKw)).font(.system(size: 34, weight: .semibold, design: .rounded)).monospacedDigit()
                Text("kW").font(.subheadline).foregroundStyle(NowInk.secondary)
                Spacer()
                if let to = detail.toTargetPct, let target = detail.targetPct {
                    Text("\(Int(to.rounded()))% to \(Int(target.rounded()))%").font(.subheadline).foregroundStyle(NowInk.secondary)
                }
            }
            if detail.haveLiveData {
                LazyVGrid(columns: [GridItem(.flexible(), alignment: .leading), GridItem(.flexible(), alignment: .leading)], alignment: .leading, spacing: 10) {
                    if let r = detail.remaining { field("Time left", "about \(NowFormat.duration(r))") }
                    if let e = detail.elapsed { field("Elapsed", NowFormat.duration(e)) }
                    if let kwh = detail.energyKwh { field("Energy added", String(format: "%.1f kWh", kwh)) }
                    if let km = detail.rangeAddedKm { field("Range added", distance(km: km)) }
                    if let rate = detail.rateKmPerHour { field("Rate", "\(distance(km: rate))/h") }
                    if let cost = detail.cost { field("Cost so far", NowFormat.costText(cost)) }
                }
            } else {
                Text("Waiting for the first reading from the charger.").font(.footnote).foregroundStyle(NowInk.secondary)
            }
        }
        .padding(20)
        .background(.background, in: RoundedRectangle(cornerRadius: 20))
        .accessibilityElement(children: .combine)
    }

    private func field(_ label: String, _ value: String) -> some View {
        VStack(alignment: .leading, spacing: 1) {
            Text(label).font(.caption).foregroundStyle(NowInk.secondary)
            Text(value).font(.subheadline.weight(.medium)).monospacedDigit()
        }
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

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Label(title, systemImage: "steeringwheel").font(.subheadline.weight(.semibold))
            HStack(alignment: .firstTextBaseline, spacing: 6) {
                Text(distanceText).font(.system(size: 30, weight: .semibold, design: .rounded)).monospacedDigit()
            }
            LazyVGrid(columns: [GridItem(.flexible(), alignment: .leading), GridItem(.flexible(), alignment: .leading)], alignment: .leading, spacing: 10) {
                if let e = detail.elapsed { field("Time", NowFormat.duration(e)) }
                if let v = detail.avgSpeedMph { field("Average speed", speed(mph: v)) }
                if let kwh = detail.energyKwh { field("Energy used", String(format: "%.1f kWh", kwh)) }
                if let eff = detail.miPerKwh { field("Efficiency", efficiency(eff)) }
                if let c = detail.estimatedCost { field("Cost", "~\(NowFormat.money(c, currency: detail.currency ?? "")) (estimate)") }
            }
        }
        .padding(20)
        .background(.background, in: RoundedRectangle(cornerRadius: 20))
        .accessibilityElement(children: .combine)
    }

    private var title: String {
        switch detail.kind {
        case .now: return "Driving now"
        case .last(let ended):
            guard let ended else { return "Last drive" }
            let f = RelativeDateTimeFormatter()
            f.unitsStyle = .full
            return "Last drive, \(f.localizedString(for: ended, relativeTo: clock))"
        }
    }

    private var distanceText: String {
        Measurement(value: detail.distanceMi, unit: UnitLength.miles)
            .formatted(.measurement(width: .abbreviated, usage: .road, numberFormatStyle: .number.precision(.fractionLength(1))))
    }

    private func speed(mph: Double) -> String {
        Measurement(value: mph, unit: UnitSpeed.milesPerHour).formatted(.measurement(width: .abbreviated, usage: .asProvided, numberFormatStyle: .number.precision(.fractionLength(0))))
    }

    /// mi/kWh, or km/kWh on a metric phone.
    private func efficiency(_ miPerKwh: Double) -> String {
        let usesMetric = Locale.current.measurementSystem != .us && Locale.current.measurementSystem != .uk
        return usesMetric ? String(format: "%.2f km/kWh", miPerKwh * 1.609344) : String(format: "%.2f mi/kWh", miPerKwh)
    }

    private func field(_ label: String, _ value: String) -> some View {
        VStack(alignment: .leading, spacing: 1) {
            Text(label).font(.caption).foregroundStyle(NowInk.secondary)
            Text(value).font(.subheadline.weight(.medium)).monospacedDigit()
        }
    }
}
