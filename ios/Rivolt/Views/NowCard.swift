import SwiftUI
import UIKit

/// The Now card, as the web's hero: the vehicle and what it is doing with a status pill, the battery as an arc gauge
/// beside the figures that matter right now (the charge limit while charging, the speed while driving, the range), a
/// Power and Time left readout while charging, and how fresh it all is. A glow only while it is live. Read-only.
/// Units follow the phone's locale. `clock` is injected so previews and the CI snapshot render the same text every time.
struct NowCard: View {
    let vehicle: Vehicle
    let summary: NowSummary
    var clock: Date = .now

    @Environment(\.dynamicTypeSize) private var typeSize

    private var charging: Bool { summary.stateLabel == "Charging" }
    private var driving: Bool { summary.stateLabel == "Driving" }
    private var isLive: Bool { summary.freshness == .fresh }
    private var critical: Bool { (summary.socPct ?? 100) < 15 && summary.socPct != nil }

    private var liveKind: ThemeLive {
        guard isLive else { return .none }
        return charging ? .charging : driving ? .driving : .none
    }

    private var tone: ThemeGaugeTone {
        if critical { return .critical }
        return summary.freshness == .fresh || summary.freshness == .stale ? .ok : .idle
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            header
            gaugeRow
            if charging { chargingRows }
            Text(footer).themeText(.subline)
        }
        .themeHero(live: liveKind, critical: critical)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(spoken)
    }

    // MARK: Header

    private var header: some View {
        HStack(alignment: .top, spacing: 8) {
            VStack(alignment: .leading, spacing: 2) {
                Text(vehicle.name).themeText(.label)
                Text(summary.stateLabel).themeText(.value)
            }
            Spacer(minLength: 8)
            let pill = Self.pill(for: summary, clock: clock)
            ThemePill(text: pill.text, tone: pill.tone)
        }
    }

    // MARK: Gauge and figures

    private var gaugeRow: some View {
        let layout = typeSize.isAccessibilitySize ? AnyLayout(VStackLayout(alignment: .leading, spacing: 16)) : AnyLayout(HStackLayout(alignment: .center, spacing: 24))
        return layout {
            ThemeGauge(value: summary.socPct ?? 0, limit: charging ? summary.chargeTargetPct : nil, live: charging && isLive, tone: tone) {
                HStack(alignment: .firstTextBaseline, spacing: 2) {
                    Text(summary.socPct.map { "\(Int($0.rounded()))" } ?? "–")
                        .themeText(.hero, color: critical ? Theme.critical : summary.socPct == nil ? Theme.textTertiary : nil)
                    Text("%").themeText(.value, color: Theme.textSecondary)
                }
            }
            VStack(alignment: .leading, spacing: 12) {
                if charging, let target = summary.chargeTargetPct {
                    figure("Charge limit", "\(Int(target.rounded()))", "%")
                }
                if driving, let kph = summary.speedKph {
                    let speed = NowFormat.usesMetric ? kph : kph / 1.609344
                    figure("Speed", speed.formatted(.number.precision(.fractionLength(0))), NowFormat.usesMetric ? "km/h" : "mph")
                }
                if let km = summary.rangeKm {
                    let range = NowFormat.usesMetric ? km : km / 1.609344
                    figure("Range", range.formatted(.number.precision(.fractionLength(0))), NowFormat.usesMetric ? "km" : "mi")
                }
            }
        }
    }

    private func figure(_ label: String, _ value: String, _ unit: String) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(label).themeText(.label)
            HStack(alignment: .firstTextBaseline, spacing: 4) {
                Text(value).themeText(.title)
                Text(unit).themeText(.value, color: Theme.textSecondary)
            }
        }
    }

    // MARK: Charging readout

    private var timeLeft: String? {
        guard let soc = summary.socPct,
              let minutes = ChargingActivityMath.minutesToTarget(socPct: soc, targetPct: summary.chargeTargetPct, packKwh: summary.packKwh, powerKw: summary.chargePowerKw)
        else { return nil }
        return Self.eta(minutes: minutes)
    }

    @ViewBuilder
    private var chargingRows: some View {
        VStack(spacing: 8) {
            if let kw = summary.chargePowerKw { readout("Power", "\(NowFormat.oneDecimal(kw)) kW") }
            if let t = timeLeft { readout("Time left", "\(t) · est.") }
            if let added = summary.chargeAddedPct {
                readout("This charge", "+\(Int(added.rounded()))%")
            }
        }
    }

    private func readout(_ label: String, _ value: String) -> some View {
        HStack(alignment: .firstTextBaseline) {
            Text(label).themeText(.value, color: Theme.textSecondary)
            Spacer(minLength: 12)
            Text(value).font(Theme.mono(ThemeTokens.typeValue, .medium, relativeTo: .body)).foregroundStyle(Theme.textPrimary).multilineTextAlignment(.trailing)
        }
    }

    // MARK: Footer

    private var footer: String {
        switch summary.freshness {
        case .needsReconnect: return "Reconnect your Rivian account on the web to see live data."
        case .paused: return "Recording is paused for this truck."
        case .fresh, .stale, .unknown:
            guard let seen = summary.lastSeen else { return "No data yet" }
            let ago = NowFormat.ago(clock.timeIntervalSince(seen))
            let base = summary.stateLabel == "Asleep" ? "Asleep · updated \(ago)" : "Updated \(ago)"
            return summary.freshness == .stale ? "\(base), may be out of date" : base
        }
    }

    private var spoken: String {
        var parts = ["\(vehicle.name). \(summary.stateLabel)."]
        if let soc = summary.socPct {
            parts.append("Charge level \(Int(soc.rounded())) percent" + (charging ? summary.chargeTargetPct.map { ", charge limit \(Int($0.rounded())) percent." } ?? "." : "."))
        }
        if let km = summary.rangeKm {
            parts.append("Range \(NowFormat.distance(mi: km / 1.609344, spoken: true)).")
        }
        if driving, let kph = summary.speedKph { parts.append("Speed \(NowFormat.speed(mph: kph / 1.609344, spoken: true)).") }
        if charging {
            if let kw = summary.chargePowerKw { parts.append("\(NowFormat.oneDecimal(kw)) kilowatts.") }
            if let t = timeLeft { parts.append("About \(t.replacingOccurrences(of: "~", with: "")) left, an estimate.") }
        }
        parts.append(footer + ".")
        return parts.joined(separator: " ")
    }

    // MARK: Pill and wording

    /// Live while the data is current to within two minutes, Asleep while a sleeping truck's rare frames are expected,
    /// otherwise how long ago it was last seen (amber once it is older than the truck's usual cadence).
    static func pill(for summary: NowSummary, clock: Date) -> (text: String, tone: ThemePill.Tone) {
        switch summary.freshness {
        case .needsReconnect: return ("Offline", .neutral)
        case .paused: return ("Paused", .neutral)
        case .unknown: return ("No data", .neutral)
        case .fresh, .stale:
            guard let seen = summary.lastSeen else { return ("No data", .neutral) }
            let age = clock.timeIntervalSince(seen)
            if summary.freshness == .fresh, summary.stateLabel == "Asleep" { return ("Asleep", .neutral) }
            if summary.freshness == .fresh, age <= 120 { return ("Live", .live) }
            return ("Last seen \(NowFormat.ago(age))", summary.freshness == .stale ? .stale : .neutral)
        }
    }

    /// "~42 min", "~5 h 6 m": a time to the charge limit, never more precise than the estimate.
    static func eta(minutes: Int) -> String {
        if minutes < 60 { return "~\(max(1, minutes)) min" }
        let h = minutes / 60, m = minutes % 60
        return m == 0 ? "~\(h) h" : "~\(h) h \(m) m"
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

    /// "+22% this charge", with the peak only when it is clearly above the
    /// current power (a peak of 11.2 next to 11.1 kW is noise).
    static func chargeLine(added: Double, peakKw: Double?, powerKw: Double?) -> String {
        var s = "+\(Int(added.rounded()))% this charge"
        if let peak = peakKw, peak > (powerKw ?? 0) * 1.1 { s += String(format: " · peak %.1f kW", peak) }
        return s
    }
}
