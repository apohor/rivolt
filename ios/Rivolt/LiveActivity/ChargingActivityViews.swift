import SwiftUI

/// The Lock Screen and banner view of the charging Live Activity, and the pieces the Dynamic Island
/// reuses, in the web's new look (the Theme layer): the battery arc, the status in the Now pill's tones and the
/// readouts in Geist Mono, as the web's hero readouts. Read-only: a tap just opens the app. Shared by the widget
/// extension and (for CI renders) the app's tests.
struct ChargingLockScreenView: View {
    let state: ChargingActivityAttributes.ContentState
    let targetPct: Int?
    /// No update has arrived for a while (the system marks the activity stale).
    let isStale: Bool
    /// A test with fixed numbers: the card says so.
    var isTest = false

    var body: some View {
        HStack(alignment: .center, spacing: 3.5 * Theme.unit) {
            ChargingGauge(state: state, targetPct: targetPct, isStale: isStale, width: 64)
            VStack(alignment: .leading, spacing: Theme.unit / 2) {
                ChargingStatus(isStale: isStale, isTest: isTest)
                Text(ChargingActivityText.power(state))
                    .font(Theme.mono(22, .semibold))
                    .foregroundStyle(Theme.textPrimary)
                if let line = ChargingActivityText.untilTarget(state.minutesToTarget, targetPct: targetPct) {
                    Text(line)
                        .font(Theme.sans(ThemeTokens.typeSubline))
                        .foregroundStyle(Theme.textSecondary)
                }
            }
            Spacer(minLength: 0)
        }
        .padding(.horizontal, 4 * Theme.unit)
        .padding(.vertical, 3 * Theme.unit)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(ChargingActivityText.spoken(state, targetPct: targetPct, isStale: isStale))
    }
}

/// The battery as the Theme's arc with the charge level in the centre: the normal fill and its glow while live, the
/// idle fill once the activity is stale.
struct ChargingGauge: View {
    let state: ChargingActivityAttributes.ContentState
    let targetPct: Int?
    let isStale: Bool
    let width: CGFloat

    var body: some View {
        let number = width * 0.28
        ThemeGauge(value: Double(state.socPct), limit: targetPct.map(Double.init), live: !isStale,
                   tone: isStale ? .idle : .ok, width: width) {
            HStack(alignment: .firstTextBaseline, spacing: 0) {
                Text("\(state.socPct)")
                    .font(Theme.sans(number, .semibold))
                    .monospacedDigit()
                    .tracking(ThemeTokens.typeNumberTracking * number)
                    .foregroundStyle(Theme.textPrimary)
                Text("%")
                    .font(Theme.sans(number * 0.5))
                    .foregroundStyle(Theme.textSecondary)
            }
            .lineLimit(1)
            .minimumScaleFactor(0.7)
        }
    }
}

/// "Charging" in the accent, or "May be out of date" in the estimate amber, as the web's Now pill.
struct ChargingStatus: View {
    let isStale: Bool
    var isTest = false

    var body: some View {
        Label {
            Text(isStale ? "May be out of date" : (isTest ? "Charging (test numbers)" : "Charging"))
        } icon: {
            Image(systemName: isStale ? "clock.badge.exclamationmark" : "bolt.fill")
        }
        .font(Theme.sans(ThemeTokens.typeCaption, .medium))
        .foregroundStyle(isStale ? Theme.estimate : Theme.accent)
        .lineLimit(1)
    }
}

// MARK: Dynamic Island (always on black: the Theme's dark values)

/// Expanded, leading: the battery arc.
struct ChargingIslandLeading: View {
    let state: ChargingActivityAttributes.ContentState
    let targetPct: Int?
    let isStale: Bool

    var body: some View {
        ChargingGauge(state: state, targetPct: targetPct, isStale: isStale, width: 56)
    }
}

/// Expanded, trailing: the power and the status.
struct ChargingIslandTrailing: View {
    let state: ChargingActivityAttributes.ContentState
    let isStale: Bool

    var body: some View {
        VStack(alignment: .trailing, spacing: Theme.unit / 2) {
            Text(ChargingActivityText.power(state))
                .font(Theme.mono(20, .semibold))
                .foregroundStyle(Theme.textPrimary)
            ChargingStatus(isStale: isStale)
        }
    }
}

/// Expanded, bottom: the time to the limit, or what is happening.
struct ChargingIslandBottom: View {
    let state: ChargingActivityAttributes.ContentState
    let targetPct: Int?

    var body: some View {
        Text(ChargingActivityText.untilTarget(state.minutesToTarget, targetPct: targetPct) ?? "Charging")
            .font(Theme.sans(ThemeTokens.typeSubline))
            .foregroundStyle(Theme.textSecondary)
    }
}

/// Compact and minimal: the bolt (or the clock once stale) in the status tone.
struct ChargingIslandIcon: View {
    let isStale: Bool

    var body: some View {
        Image(systemName: isStale ? "clock.badge.exclamationmark" : "bolt.fill")
            .foregroundStyle(isStale ? Theme.estimate : Theme.accent)
    }
}

/// Compact, trailing: the charge level.
struct ChargingIslandLevel: View {
    let state: ChargingActivityAttributes.ContentState

    var body: some View {
        Text("\(state.socPct)%")
            .font(Theme.sans(ThemeTokens.typeValue, .semibold))
            .monospacedDigit()
            .foregroundStyle(Theme.textPrimary)
    }
}

enum ChargingActivityText {
    /// "11.1 kW".
    static func power(_ s: ChargingActivityAttributes.ContentState) -> String {
        String(format: "%.1f kW", s.powerKw)
    }

    /// "about 42 min to 80%", or "42 min" without a limit; nil when unknown.
    static func untilTarget(_ minutes: Int?, targetPct: Int?) -> String? {
        // The server sends 0 when it can't tell (never a missing key): that is "unknown", not "0 min".
        guard let m = minutes, m > 0 else { return nil }
        let t = m >= 60 ? "\(m / 60) h \(m % 60) min" : "\(m) min"
        if let target = targetPct { return "about \(t) to \(target)%" }
        return "about \(t)"
    }

    static func spoken(_ s: ChargingActivityAttributes.ContentState, targetPct: Int?, isStale: Bool) -> String {
        var parts = ["Charging, \(s.socPct) percent", String(format: "%.1f kilowatts", s.powerKw)]
        if let line = untilTarget(s.minutesToTarget, targetPct: targetPct) { parts.append(line) }
        if isStale { parts.append("may be out of date") }
        return parts.joined(separator: ", ")
    }
}
