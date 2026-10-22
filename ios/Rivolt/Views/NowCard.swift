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
    /// Home, Away or Fast from the latest charge's ledger place, when it is known (no location is used for it).
    var place: String?

    @Environment(\.dynamicTypeSize) private var typeSize

    private var offline: Bool { summary.presence == .offline || summary.freshness == .needsReconnect }
    private var charging: Bool { summary.kind == .charging && !offline }
    private var driving: Bool { summary.kind == .driving && !offline }
    private var plugged: Bool { summary.kind == .plugged && !offline }
    private var isLive: Bool { summary.presence == .live && summary.freshness != .needsReconnect && summary.freshness != .paused }
    /// Under 10%, as on the web.
    private var critical: Bool { Self.isCritical(summary) }

    private var liveKind: ThemeLive {
        guard isLive else { return .none }
        return charging ? .charging : driving ? .driving : .none
    }

    private var tone: ThemeGaugeTone {
        if offline { return .idle }
        return critical ? .critical : .ok
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
                Text(Self.headline(for: summary, place: place)).themeText(.value)
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
            ThemeGauge(value: summary.socPct ?? 0, limit: charging || plugged ? summary.chargeTargetPct : nil, live: charging && isLive, tone: tone) {
                HStack(alignment: .firstTextBaseline, spacing: 2) {
                    Text(summary.socPct.map { "\(Int($0.rounded()))" } ?? "–")
                        .themeText(.hero, color: critical ? Theme.critical : (offline || summary.socPct == nil) ? Theme.textTertiary : nil)
                    Text("%").themeText(.value, color: Theme.textSecondary)
                }
            }
            VStack(alignment: .leading, spacing: 12) {
                if charging || plugged, let target = summary.chargeTargetPct {
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
            if let kw = summary.chargePowerKw { readout("Power", "\(kw.formatted(.number.precision(.fractionLength(kw < 10 ? 1 : 0)))) kW") }
            if let t = timeLeft { readout("Time left", "\(t) · est.") }
            if let added = summary.chargeAddedPct, let pack = summary.packKwh, added > 0 {
                readout("Added", "≈ \(NowFormat.oneDecimal(added / 100 * pack)) kWh")
            }
        }
    }

    private func readout(_ label: String, _ value: String) -> some View {
        HStack(alignment: .firstTextBaseline) {
            Text(label).themeText(.value, color: Theme.textSecondary)
            Spacer(minLength: 12)
            Text(value).themeText(.value, color: Theme.textPrimary).multilineTextAlignment(.trailing)
        }
    }

    // MARK: Footer

    private var footer: String { Self.footer(for: summary, clock: clock) }

    private var spoken: String {
        var parts = ["\(vehicle.name). \(Self.headline(for: summary, place: place))."]
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
        if summary.freshness == .needsReconnect || summary.presence == .offline { return ("Offline", .neutral) }
        if summary.freshness == .paused { return ("Paused", .neutral) }
        guard let age = summary.ageSec else { return ("No data", .neutral) }
        if summary.presence == .live, summary.kind == .asleep || (summary.kind == .plugged && summary.sleeping) { return ("Asleep", .neutral) }
        if summary.presence == .live, age <= 120 { return ("Live", .live) }
        return ("Last seen \(NowFormat.ago(age))", summary.presence == .stale ? .stale : .neutral)
    }

    /// The warnings under the hero, at most two: a low battery, a truck that keeps waking, a low tire.
    static func alerts(for s: NowSummary, vehicleName: String) -> [NowAlert] {
        var out: [NowAlert] = []
        if isCritical(s), let soc = s.socPct {
            out.append(NowAlert(id: "critical-soc", tone: .rose, text: "Battery at \(Int(soc.rounded()))%. Plan a charge soon."))
        }
        if let n = s.failedSleeps6h, n >= 3 {
            out.append(NowAlert(id: "waking", tone: .amber, text: "\(vehicleName) woke up \(n) times in the last 6 hours."))
        }
        if let bar = s.tireMinBar, bar > 0, bar < 2.2 {
            out.append(NowAlert(id: "tire", tone: .amber, text: "Lowest tire \(bar.formatted(.number.precision(.fractionLength(1)))) bar."))
        }
        return Array(out.prefix(2))
    }

    /// Under 10% and still in touch (an offline truck's last level isn't a warning).
    static func isCritical(_ s: NowSummary) -> Bool {
        guard let soc = s.socPct, soc > 0, soc < 10 else { return false }
        return s.presence != .offline
    }

    /// What the truck is doing, in the web's words.
    static func headline(for s: NowSummary, place: String? = nil) -> String {
        if s.presence == .offline || s.freshness == .needsReconnect { return "Not heard from recently" }
        switch s.kind {
        case .driving: return "Driving"
        case .charging:
            if (s.chargePowerKw ?? 0) <= 0.3 { return "Starting to charge…" }
            if max(s.chargePeakKw ?? 0, s.chargePowerKw ?? 0) >= 50 { return "Fast charging" }
            return place == "Home" ? "Charging at home" : "Charging"
        case .plugged:
            if let soc = s.socPct, let limit = s.chargeTargetPct, soc >= limit - 1.5 { return "Plugged in, holding \(Int(limit.rounded()))%" }
            return "Plugged in"
        case .asleep: return "Asleep"
        case .parked: return "Parked"
        case .unknown: return s.stateLabel
        }
    }

    /// How fresh it is, since when it has been in this state and how the level moved over it.
    static func footer(for s: NowSummary, clock: Date) -> String {
        switch s.freshness {
        case .needsReconnect: return "Reconnect your Rivian account on the web to see live data."
        case .paused: return "Recording is paused for this truck."
        case .fresh, .stale, .unknown:
            guard let seen = s.lastSeen, let age = s.ageSec else { return "No data yet" }
            if s.presence == .offline {
                return "Last contact \(seen.formatted(date: .omitted, time: .shortened)) (\(NowFormat.ago(age).replacingOccurrences(of: " ago", with: "")) ago)"
            }
            var parts = [s.kind == .asleep ? "Asleep · updated \(NowFormat.ago(age))" : "Updated \(NowFormat.ago(age))"]
            let stateful = [.parked, .plugged, .asleep].contains(s.kind)
            if stateful, let since = s.since {
                var text = "since \(since.formatted(date: .omitted, time: .shortened))"
                if let d = s.socChangeSinceState, abs(d) >= 0.5 { text += " (\(d > 0 ? "+" : "−")\(Int(abs(d).rounded()))%)" }
                parts.append(text)
            }
            return parts.joined(separator: " · ")
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

struct NowAlert: Equatable, Identifiable {
    enum Tone { case rose, amber }
    let id: String
    let tone: Tone
    let text: String
}

/// A warning under the hero, rose for the battery and amber for the rest, in the web's tones.
struct NowAlertBanner: View {
    let alert: NowAlert

    private var edge: Color { alert.tone == .rose ? Theme.criticalBorder : Theme.estimateFill }
    private var ink: Color { alert.tone == .rose ? Theme.critical : Theme.estimate }

    var body: some View {
        Text(alert.text)
            .themeText(.value, color: ink)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, 4 * Theme.unit).padding(.vertical, 3 * Theme.unit)
            .background(edge.opacity(0.08), in: RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous).strokeBorder(edge.opacity(0.5), lineWidth: 1))
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(alert.text)
    }
}
