import SwiftUI
import UIKit

/// Text colours that keep 4.5:1 on the card in light mode: the system green and
/// orange are about 2.2:1 on white and the secondary label about 3.4:1. Dark
/// mode keeps the system colours (10:1 on black). The bar and icons keep the
/// bright colours; only words use these.
private enum NowInk {
    static let secondary = Color(uiColor: UIColor { trait in
        trait.userInterfaceStyle == .dark ? UIColor.secondaryLabel : UIColor(white: 0.36, alpha: 1)
    })
    static let charging = Color(uiColor: UIColor { trait in
        trait.userInterfaceStyle == .dark ? UIColor.systemGreen : UIColor(red: 27 / 255, green: 122 / 255, blue: 49 / 255, alpha: 1)
    })
    static let stale = Color(uiColor: UIColor { trait in
        trait.userInterfaceStyle == .dark ? UIColor.systemOrange : UIColor(red: 168 / 255, green: 84 / 255, blue: 0, alpha: 1)
    })
}

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
                        Text("range").font(.footnote).foregroundStyle(NowInk.secondary)
                    }
                }
            }

            if let soc = summary.socPct {
                SoCBar(socPct: soc, targetPct: summary.chargeTargetPct)
            }

            VStack(alignment: .leading, spacing: 6) {
                let status = Self.status(for: summary)
                Label(status.headline, systemImage: statusIcon)
                    .font(.headline)
                    .foregroundStyle(summary.stateLabel == "Charging" ? NowInk.charging : Color.primary)
                if let detail = status.detail {
                    Text(detail)
                        .font(.subheadline)
                        .foregroundStyle(NowInk.secondary)
                }
                if let added = summary.chargeAddedPct {
                    Text(Self.chargeLine(added: added, peakKw: summary.chargePeakKw, powerKw: summary.chargePowerKw))
                        .font(.subheadline)
                        .foregroundStyle(NowInk.secondary)
                }
                if let temp = summary.outsideTempC {
                    Text("\(Measurement(value: temp, unit: UnitTemperature.celsius).formatted(.measurement(width: .abbreviated, usage: .weather))) outside")
                        .font(.subheadline)
                        .foregroundStyle(NowInk.secondary)
                }
            }
            .accessibilityElement(children: .combine)

            FreshnessLine(summary: summary, clock: clock)

            Text("\(vehicle.model) · VIN …\(vehicle.vin.suffix(6))")
                .font(.caption)
                .foregroundStyle(NowInk.secondary)
                .monospaced()
        }
        .padding(20)
        .background(.background, in: RoundedRectangle(cornerRadius: 20))
    }

    /// The status in two lines, so a long one never leaves a time orphaned on
    /// the next line: what the truck is doing (and its power), then the charge
    /// limit and since when.
    struct StatusText: Equatable {
        let headline: String
        let detail: String?
    }

    static func status(for summary: NowSummary) -> StatusText {
        var head = [summary.stateLabel]
        if let kw = summary.chargePowerKw { head.append(String(format: "%.1f kW", kw)) }
        var detail: [String] = []
        if let target = summary.chargeTargetPct, summary.stateLabel == "Charging" {
            detail.append("to \(Int(target.rounded()))%")
        }
        if let since = summary.since {
            detail.append("since \(since.formatted(date: .omitted, time: .shortened))")
        }
        return StatusText(headline: head.joined(separator: " · "), detail: detail.isEmpty ? nil : detail.joined(separator: " · "))
    }

    private var statusIcon: String {
        switch summary.stateLabel {
        case "Charging": return "bolt.fill"
        case "Driving": return "steeringwheel"
        case "Asleep": return "moon.zzz"
        default: return summary.stateLabel.hasPrefix("Plugged") ? "powerplug" : "parkingsign"
        }
    }

    /// "+22% this charge", with the peak only when it is clearly above the
    /// current power (a peak of 11.2 next to 11.1 kW is noise).
    static func chargeLine(added: Double, peakKw: Double?, powerKw: Double?) -> String {
        var s = "+\(Int(added.rounded()))% this charge"
        if let peak = peakKw, peak > (powerKw ?? 0) * 1.1 { s += String(format: " · peak %.1f kW", peak) }
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
        .accessibilityValue(targetPct.map { "Limit \(Int($0.rounded())) percent" } ?? "")
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
                .font(.footnote).foregroundStyle(NowInk.stale)
        case .paused:
            Label("Recording is paused for this truck.", systemImage: "pause.circle")
                .font(.footnote).foregroundStyle(NowInk.secondary)
        case .fresh, .stale, .unknown:
            if let seen = summary.lastSeen {
                Label(updatedText(seen), systemImage: summary.freshness == .stale ? "clock.badge.exclamationmark" : "clock")
                    .font(.footnote)
                    .foregroundStyle(summary.freshness == .stale ? NowInk.stale : NowInk.secondary)
            } else {
                Label("No data yet", systemImage: "clock").font(.footnote).foregroundStyle(NowInk.secondary)
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
