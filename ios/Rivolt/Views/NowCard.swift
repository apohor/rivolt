import SwiftUI

/// The Now card: charge level, range and what the truck is doing, with how
/// fresh that is. Read-only. Units follow the phone's locale (miles or km,
/// °F or °C). `clock` is injected so previews and the CI snapshot render the
/// same text every time.
struct NowCard: View {
    let vehicle: Vehicle
    let summary: NowSummary
    var clock: Date = .now

    var body: some View {
        VStack(alignment: .leading, spacing: 20) {
            HStack(alignment: .firstTextBaseline) {
                Text(summary.socPct.map { "\(Int($0.rounded()))%" } ?? "–")
                    .font(.system(size: 72, weight: .semibold, design: .rounded))
                    .monospacedDigit()
                Spacer()
                if let km = summary.rangeKm {
                    VStack(alignment: .trailing, spacing: 2) {
                        Text(Measurement(value: km, unit: UnitLength.kilometers)
                            .formatted(.measurement(width: .abbreviated, usage: .road,
                                                    numberFormatStyle: .number.precision(.fractionLength(0)))))
                            .font(.title2.weight(.semibold))
                            .monospacedDigit()
                        Text("range").font(.footnote).foregroundStyle(.secondary)
                    }
                }
            }

            if let soc = summary.socPct {
                SoCBar(socPct: soc, targetPct: summary.chargeTargetPct)
            }

            VStack(alignment: .leading, spacing: 6) {
                Label(statusLine, systemImage: statusIcon)
                    .font(.headline)
                    .foregroundStyle(summary.stateLabel == "Charging" ? .green : .primary)
                if let added = summary.chargeAddedPct {
                    Text(chargeLine(added: added))
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                }
                if let temp = summary.outsideTempC {
                    Text("\(Measurement(value: temp, unit: UnitTemperature.celsius).formatted(.measurement(width: .abbreviated, usage: .weather))) outside")
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                }
            }

            FreshnessLine(summary: summary, clock: clock)

            Text("\(vehicle.model) · VIN …\(vehicle.vin.suffix(6))")
                .font(.caption)
                .foregroundStyle(.tertiary)
                .monospaced()
        }
        .padding(20)
        .background(.background, in: RoundedRectangle(cornerRadius: 20))
    }

    private var statusLine: String {
        var parts = [summary.stateLabel]
        if let kw = summary.chargePowerKw { parts.append(String(format: "%.1f kW", kw)) }
        if let target = summary.chargeTargetPct, summary.stateLabel == "Charging" {
            parts.append("to \(Int(target.rounded()))%")
        }
        if let since = summary.since {
            parts.append("since \(since.formatted(date: .omitted, time: .shortened))")
        }
        return parts.joined(separator: " · ")
    }

    private var statusIcon: String {
        switch summary.stateLabel {
        case "Charging": return "bolt.fill"
        case "Driving": return "steeringwheel"
        case "Asleep": return "moon.zzz"
        default: return summary.stateLabel.hasPrefix("Plugged") ? "powerplug" : "parkingsign"
        }
    }

    private func chargeLine(added: Double) -> String {
        var s = "+\(Int(added.rounded()))% this charge"
        if let peak = summary.chargePeakKw { s += String(format: " · peak %.1f kW", peak) }
        return s
    }
}

private struct SoCBar: View {
    let socPct: Double
    let targetPct: Double?

    var body: some View {
        GeometryReader { geo in
            ZStack(alignment: .leading) {
                Capsule().fill(.quaternary)
                Capsule().fill(tint)
                    .frame(width: geo.size.width * min(max(socPct, 0), 100) / 100)
                if let target = targetPct {
                    Rectangle().fill(.secondary)
                        .frame(width: 2)
                        .offset(x: geo.size.width * min(max(target, 0), 100) / 100 - 1)
                }
            }
        }
        .frame(height: 10)
        .accessibilityElement()
        .accessibilityLabel("Charge level \(Int(socPct.rounded())) percent")
    }

    private var tint: Color {
        switch socPct {
        case ..<15: return .red
        case ..<30: return .orange
        default: return .green
        }
    }
}

private struct FreshnessLine: View {
    let summary: NowSummary
    let clock: Date

    var body: some View {
        switch summary.freshness {
        case .needsReconnect:
            Label("Reconnect your Rivian account on the web to see live data.", systemImage: "exclamationmark.triangle")
                .font(.footnote).foregroundStyle(.orange)
        case .paused:
            Label("Recording is paused for this truck.", systemImage: "pause.circle")
                .font(.footnote).foregroundStyle(.secondary)
        case .fresh, .stale, .unknown:
            if let seen = summary.lastSeen {
                Label(updatedText(seen), systemImage: summary.freshness == .stale ? "clock.badge.exclamationmark" : "clock")
                    .font(.footnote)
                    .foregroundStyle(summary.freshness == .stale ? .orange : .secondary)
            } else {
                Label("No data yet", systemImage: "clock").font(.footnote).foregroundStyle(.secondary)
            }
        }
    }

    private func updatedText(_ seen: Date) -> String {
        let rel = RelativeDateTimeFormatter()
        rel.unitsStyle = .full
        let ago = rel.localizedString(for: seen, relativeTo: clock)
        return summary.freshness == .stale ? "Updated \(ago), may be out of date" : "Updated \(ago)"
    }
}
