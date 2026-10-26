import SwiftUI
import WidgetKit

/// The widgets' views, shared by the widget extension and (for CI renders) the app's tests.
/// Read-only: no buttons and no intents; a tap just opens the app.
struct NowWidgetEntryView: View {
    let entry: NowEntry
    @Environment(\.widgetFamily) private var family

    var body: some View {
        NowWidgetView(entry: entry, family: family)
    }
}

struct NowWidgetView: View {
    let entry: NowEntry
    let family: WidgetFamily

    var body: some View {
        switch entry.content {
        case .signedOut:
            PlaceholderView(text: "Open Rivolt to sign in", family: family)
        case .unavailable:
            PlaceholderView(text: "No data yet. Open Rivolt.", family: family)
        case .data(let s):
            switch family {
            case .accessoryCircular: CircularView(snapshot: s, at: entry.date)
            case .accessoryRectangular: RectangularView(snapshot: s, at: entry.date)
            case .accessoryInline: InlineView(snapshot: s, at: entry.date)
            case .systemMedium: HomeFamilyView(snapshot: s, at: entry.date, wide: true)
            default: HomeFamilyView(snapshot: s, at: entry.date, wide: false)
            }
        }
    }
}

/// The range in the account's distance unit (as the app's Now card), whole, rounded as the web does.
private func rangeText(_ km: Double, spoken: Bool = false) -> String {
    let miles = !DisplayUnits.distanceMetric
    let unit = spoken ? (miles ? "miles" : "kilometres") : (miles ? "mi" : "km")
    return "\(NumberText.fixed(miles ? km / 1.609344 : km, 0)) \(unit)"
}

private func socText(_ s: WidgetSnapshot) -> String { "\(Int(s.socPct.rounded()))%" }

private func spoken(_ s: WidgetSnapshot, at date: Date) -> String {
    var parts = ["Charge \(Int(s.socPct.rounded())) percent", "range \(rangeText(s.rangeKm, spoken: true))"]
    parts.append(s.status(at: date).text)
    return parts.joined(separator: ", ")
}

/// The status line's colour, as the web's Now pill: live (charging) in the accent, out of date in the estimate amber,
/// anything else secondary.
private func toneColor(_ tone: WidgetSnapshot.Tone) -> Color {
    switch tone {
    case .charging: return Theme.accent
    case .stale: return Theme.estimate
    case .normal: return Theme.textSecondary
    }
}

/// The gauge's fill, as the Now card's: low (under 10%, as the web) in the critical gradient, out of date in the idle
/// one, otherwise the normal one.
private func gaugeTone(_ s: WidgetSnapshot, at date: Date) -> ThemeGaugeTone {
    if s.isStale(at: date) || s.attention != .none { return .idle }
    if s.socPct > 0, s.socPct < 10 { return .critical }
    return .ok
}

/// The home-screen widgets' background: the web's Now hero (the card over the page, with its light pool in the top
/// left). Also used by the CI renders, which draw outside a widget.
struct NowWidgetBackground: View {
    var body: some View {
        ZStack {
            Theme.background
            Theme.card.opacity(0.6)
            RadialGradient(colors: [Theme.heroTint, .clear], center: UnitPoint(x: 0.14, y: 0), startRadius: 0, endRadius: 200)
        }
    }
}

private struct HomeFamilyView: View {
    let snapshot: WidgetSnapshot
    let at: Date
    let wide: Bool

    var body: some View {
        let status = snapshot.status(at: at)
        // A charge limit belongs to a charge that is still going on, not to data that has gone stale.
        let target = (snapshot.isCharging && snapshot.attention == .none && !snapshot.isStale(at: at)) ? snapshot.chargeTargetPct : nil
        let live = snapshot.isCharging && snapshot.attention == .none && !snapshot.isStale(at: at)
        VStack(alignment: .leading, spacing: 1.5 * Theme.unit) {
            Label {
                Text(status.text)
            } icon: {
                Image(systemName: status.icon)
            }
            .font(Theme.sans(ThemeTokens.typeCaption, .medium))
            .foregroundStyle(toneColor(status.tone))
            .lineLimit(1)
            .minimumScaleFactor(0.8)

            HStack(alignment: .center, spacing: 3 * Theme.unit) {
                ThemeGauge(value: snapshot.socPct, limit: target, live: live, tone: gaugeTone(snapshot, at: at), width: wide ? 104 : 96) {
                    HStack(alignment: .firstTextBaseline, spacing: 1) {
                        Text("\(Int(snapshot.socPct.rounded()))")
                            .font(Theme.sans(wide ? 30 : 28, .semibold))
                            .monospacedDigit()
                            .tracking(ThemeTokens.typeNumberTracking * (wide ? 30 : 28))
                            .foregroundStyle(gaugeTone(snapshot, at: at) == .critical ? Theme.critical : Theme.textPrimary)
                        Text("%").font(Theme.sans(ThemeTokens.typeValue)).foregroundStyle(Theme.textSecondary)
                    }
                    .minimumScaleFactor(0.7)
                    .lineLimit(1)
                }
                if wide {
                    VStack(alignment: .leading, spacing: Theme.unit) {
                        Text("Range")
                            .font(Theme.sans(ThemeTokens.typeLabelSize, .medium))
                            .textCase(.uppercase)
                            .tracking(ThemeTokens.typeLabelTracking * ThemeTokens.typeLabelSize)
                            .foregroundStyle(Theme.textTertiary)
                        Text(rangeText(snapshot.rangeKm))
                            .font(Theme.sans(ThemeTokens.typeTitle, .semibold))
                            .monospacedDigit()
                            .tracking(ThemeTokens.typeNumberTracking * ThemeTokens.typeTitle)
                            .foregroundStyle(Theme.textPrimary)
                            .minimumScaleFactor(0.7)
                            .lineLimit(1)
                        if let target {
                            Text("to \(Int(target.rounded()))%")
                                .font(Theme.sans(ThemeTokens.typeSubline))
                                .foregroundStyle(Theme.textSecondary)
                        }
                    }
                }
            }
            .frame(maxWidth: .infinity, alignment: wide ? .leading : .center)

            if !wide {
                Text("\(rangeText(snapshot.rangeKm)) range")
                    .font(Theme.sans(ThemeTokens.typeSubline))
                    .monospacedDigit()
                    .foregroundStyle(Theme.textSecondary)
                    .frame(maxWidth: .infinity, alignment: .center)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(spoken(snapshot, at: at))
    }
}

private struct CircularView: View {
    let snapshot: WidgetSnapshot
    let at: Date

    var body: some View {
        Gauge(value: min(max(snapshot.socPct, 0), 100), in: 0...100) {
            Image(systemName: snapshot.status(at: at).icon)
        } currentValueLabel: {
            Text("\(Int(snapshot.socPct.rounded()))")
        }
        .gaugeStyle(.accessoryCircular)
        .opacity(snapshot.isStale(at: at) ? 0.5 : 1)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(spoken(snapshot, at: at))
    }
}

private struct RectangularView: View {
    let snapshot: WidgetSnapshot
    let at: Date

    var body: some View {
        let status = snapshot.status(at: at)
        VStack(alignment: .leading, spacing: 2) {
            Text("\(socText(snapshot)) · \(rangeText(snapshot.rangeKm))").font(Theme.sans(ThemeTokens.typeValue, .semibold)).monospacedDigit()
            Label(status.text, systemImage: status.icon).font(Theme.sans(ThemeTokens.typeSubline)).lineLimit(1)
            if snapshot.isCharging, snapshot.attention == .none, let target = snapshot.chargeTargetPct, !snapshot.isStale(at: at) {
                Text("to \(Int(target.rounded()))%").font(Theme.sans(ThemeTokens.typeCaption))
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(spoken(snapshot, at: at))
    }
}

private struct InlineView: View {
    let snapshot: WidgetSnapshot
    let at: Date

    var body: some View {
        Label("\(socText(snapshot)) · \(rangeText(snapshot.rangeKm))\(snapshot.isStale(at: at) ? " (old)" : "")",
              systemImage: snapshot.isStale(at: at) ? "clock.badge.exclamationmark" : (snapshot.isCharging ? "bolt.fill" : "car"))
            .accessibilityLabel(spoken(snapshot, at: at))
    }
}

private struct PlaceholderView: View {
    let text: String
    let family: WidgetFamily

    var body: some View {
        // The round lock-screen widget is too small for words: the icon, with the words for VoiceOver.
        if family == .accessoryCircular {
            Image(systemName: "bolt.car")
                .font(.title2)
                .foregroundStyle(Theme.textSecondary)
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .accessibilityLabel(text)
        } else {
            wordsView
        }
    }

    private var wordsView: some View {
        Label {
            Text(text)
        } icon: {
            Image(systemName: "bolt.car").foregroundStyle(family == .accessoryInline ? Theme.textSecondary : Theme.brandMark)
        }
            .font(family == .accessoryInline ? .body : Theme.sans(ThemeTokens.typeSubline, .medium))
            .foregroundStyle(Theme.textSecondary)
            .multilineTextAlignment(.leading)
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .center)
    }
}
