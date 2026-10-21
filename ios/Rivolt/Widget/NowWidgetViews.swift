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

private func rangeText(_ km: Double) -> String {
    Measurement(value: km, unit: UnitLength.kilometers)
        .formatted(.measurement(width: .abbreviated, usage: .road, numberFormatStyle: .number.precision(.fractionLength(0))))
}

private func socText(_ s: WidgetSnapshot) -> String { "\(Int(s.socPct.rounded()))%" }

private func spoken(_ s: WidgetSnapshot, at date: Date) -> String {
    var parts = ["Charge \(Int(s.socPct.rounded())) percent", "range \(rangeText(s.rangeKm))"]
    parts.append(s.status(at: date).text)
    return parts.joined(separator: ", ")
}

private func toneColor(_ tone: WidgetSnapshot.Tone) -> Color {
    switch tone {
    case .charging: return NowInk.charging
    case .stale: return NowInk.stale
    case .normal: return NowInk.secondary
    }
}

private struct HomeFamilyView: View {
    let snapshot: WidgetSnapshot
    let at: Date
    let wide: Bool

    var body: some View {
        let status = snapshot.status(at: at)
        VStack(alignment: .leading, spacing: 6) {
            Label(status.text, systemImage: status.icon)
                .font(.caption.weight(.semibold))
                .foregroundStyle(toneColor(status.tone))
                .lineLimit(1)
                .minimumScaleFactor(0.8)
            HStack(alignment: .firstTextBaseline) {
                Text(socText(snapshot))
                    .font(.system(size: wide ? 52 : 44, weight: .semibold, design: .rounded))
                    .monospacedDigit()
                    .minimumScaleFactor(0.7)
                    .lineLimit(1)
                if wide {
                    Spacer()
                    VStack(alignment: .trailing, spacing: 0) {
                        Text(rangeText(snapshot.rangeKm)).font(.title2.weight(.semibold)).monospacedDigit()
                        Text("range").font(.caption).foregroundStyle(NowInk.secondary)
                    }
                }
            }
            SoCBar(socPct: snapshot.socPct, targetPct: snapshot.isCharging ? snapshot.chargeTargetPct : nil, dimmed: snapshot.isStale(at: at))
            if !wide {
                Text("\(rangeText(snapshot.rangeKm)) range").font(.caption).foregroundStyle(NowInk.secondary)
            } else if let target = snapshot.chargeTargetPct, snapshot.isCharging {
                Text("to \(Int(target.rounded()))%").font(.caption).foregroundStyle(NowInk.secondary)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(spoken(snapshot, at: at))
    }
}

private struct SoCBar: View {
    let socPct: Double
    let targetPct: Double?
    let dimmed: Bool

    var body: some View {
        GeometryReader { geo in
            ZStack(alignment: .leading) {
                Capsule().fill(.quaternary)
                Capsule().fill(tint).frame(width: geo.size.width * min(max(socPct, 0), 100) / 100)
                if let target = targetPct {
                    Rectangle().fill(.secondary).frame(width: 2)
                        .offset(x: geo.size.width * min(max(target, 0), 100) / 100 - 1)
                }
            }
        }
        .frame(height: 8)
        .opacity(dimmed ? 0.5 : 1)
    }

    private var tint: Color {
        switch socPct {
        case ..<15: return .red
        case ..<30: return .orange
        default: return .green
        }
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
            Text("\(socText(snapshot)) · \(rangeText(snapshot.rangeKm))").font(.headline).monospacedDigit()
            Label(status.text, systemImage: status.icon).font(.caption).lineLimit(1)
            if snapshot.isCharging, snapshot.attention == .none, let target = snapshot.chargeTargetPct, !snapshot.isStale(at: at) {
                Text("to \(Int(target.rounded()))%").font(.caption2)
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
              systemImage: snapshot.isCharging ? "bolt.fill" : "car")
            .accessibilityLabel(spoken(snapshot, at: at))
    }
}

private struct PlaceholderView: View {
    let text: String
    let family: WidgetFamily

    var body: some View {
        Label(text, systemImage: "bolt.car")
            .font(family == .accessoryInline ? .body : .caption.weight(.semibold))
            .foregroundStyle(NowInk.secondary)
            .multilineTextAlignment(.leading)
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .center)
    }
}
