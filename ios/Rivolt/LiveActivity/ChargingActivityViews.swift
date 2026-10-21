import SwiftUI

/// The Lock Screen and banner view of the charging Live Activity, and the pieces the Dynamic Island
/// reuses. Read-only: a tap just opens the app. Shared by the widget extension and (for CI renders) the
/// app's tests.
struct ChargingLockScreenView: View {
    let state: ChargingActivityAttributes.ContentState
    let targetPct: Int?
    /// No update has arrived for a while (the system marks the activity stale).
    let isStale: Bool
    /// A test with fixed numbers: the card says so.
    var isTest = false

    var body: some View {
        HStack(alignment: .center, spacing: 14) {
            Image(systemName: isStale ? "clock.badge.exclamationmark" : "bolt.fill")
                .font(.title2)
                .foregroundStyle(isStale ? NowInk.stale : NowInk.charging)
            VStack(alignment: .leading, spacing: 2) {
                Text(isStale ? "May be out of date" : (isTest ? "Charging (test numbers)" : "Charging"))
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(isStale ? NowInk.stale : NowInk.charging)
                Text("\(state.socPct)%")
                    .font(.system(size: 34, weight: .semibold, design: .rounded))
                    .monospacedDigit()
            }
            Spacer(minLength: 8)
            VStack(alignment: .trailing, spacing: 2) {
                Text(String(format: "%.1f kW", state.powerKw))
                    .font(.headline)
                    .monospacedDigit()
                if let line = ChargingActivityText.untilTarget(state.minutesToTarget, targetPct: targetPct) {
                    Text(line).font(.caption).foregroundStyle(NowInk.secondary)
                }
            }
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 12)
        .opacity(isStale ? 0.7 : 1)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(ChargingActivityText.spoken(state, targetPct: targetPct, isStale: isStale))
    }
}

enum ChargingActivityText {
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
