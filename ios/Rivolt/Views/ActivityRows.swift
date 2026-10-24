import SwiftUI

/// "WED, OCT 21": a small-caps day label with a hairline running out to the right.
struct ActivityDayHeader: View {
    let day: Date
    var calendar: Calendar = .current

    var body: some View {
        HStack(spacing: 12) {
            Text(label).themeText(.label).accessibilityAddTraits(.isHeader)
            Rectangle().fill(Theme.border).frame(height: 1)
        }
    }

    private var label: String {
        var f = Date.FormatStyle(timeZone: calendar.timeZone).weekday(.abbreviated).month(.abbreviated).day()
        f.calendar = calendar
        return day.formatted(f)
    }
}

/// One row of the feed on its timeline rail: a glowing node (sky for a drive, the accent for a charge, warm for a fast
/// charge) and the card.
struct ActivityFeedRow: View {
    let item: ActivityItem
    var calendar: Calendar = .current

    private var nodeColor: Color {
        switch item {
        case .drive: return Theme.drive
        case .charge(let c): return ActivityLogic.placeLabel(c) == "Fast" ? Theme.fastFill : Theme.accentFill
        }
    }

    var body: some View {
        HStack(alignment: .top, spacing: 12) {
            ZStack(alignment: .top) {
                Rectangle().fill(Theme.border).frame(width: 1)
                Circle().fill(Theme.background).frame(width: 14, height: 14)
                    .overlay(Circle().strokeBorder(nodeColor, lineWidth: 3))
                    .shadow(color: nodeColor.opacity(0.5), radius: 4)
                    .padding(.top, 20)
            }
            .frame(width: 14)
            card.padding(.bottom, 12)
        }
    }

    @ViewBuilder
    private var card: some View {
        switch item {
        case .drive(let d): DriveCardContent(drive: d, calendar: calendar)
        case .charge(let c): ChargeCardContent(charge: c, calendar: calendar)
        }
    }
}

private struct DriveCardContent: View {
    let drive: ActivityDrive
    let calendar: Calendar
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        let layout = typeSize.isAccessibilitySize ? AnyLayout(VStackLayout(alignment: .leading, spacing: 8)) : AnyLayout(HStackLayout(alignment: .firstTextBaseline, spacing: 8))
        layout {
            VStack(alignment: .leading, spacing: 4) {
                HStack(spacing: 6) {
                    Image(systemName: "arrow.up.right").font(.system(size: 12, weight: .semibold)).foregroundStyle(Theme.textSecondary)
                    Text(title).themeText(.value, color: Theme.textPrimary)
                }
                Text(sub).themeText(.subline)
            }
            if !typeSize.isAccessibilitySize { Spacer(minLength: 8) }
            VStack(alignment: typeSize.isAccessibilitySize ? .leading : .trailing, spacing: 4) {
                Text(NowFormat.distance(mi: drive.distanceMi)).themeText(.value, color: Theme.textPrimary)
                if ActivityLogic.missingEnergy(drive) {
                    Text("No energy reading").themeText(.subline, color: Theme.estimate)
                } else if let c = drive.estimatedCost, c > 0, let m = NowFormat.moneyIfKnown(c, currency: drive.estimatedCurrency) {
                    Text("~\(m)").themeText(.subline, color: Theme.textBody)
                }
            }
        }
        .themeCard(padding: 3 * Theme.unit)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(ActivityLogic.spoken(drive, calendar: calendar))
    }

    private var title: String {
        if let legs = drive.roundTripLegs, legs > 1 { return "Round trip" }
        return "Drive"
    }

    private var sub: String {
        var s = ActivityLogic.timeRange(start: drive.startedAt, end: drive.endedAt, calendar: calendar)
        if let t = ActivityLogic.duration(drive) { s += " · \(ActivityLogic.shortDuration(t))" }
        if drive.hasTrailer { s += " · Trailer" }
        return s
    }
}

private struct ChargeCardContent: View {
    let charge: ActivityCharge
    let calendar: Calendar
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        let line = ActivityLogic.costLine(charge)
        let layout = typeSize.isAccessibilitySize ? AnyLayout(VStackLayout(alignment: .leading, spacing: 8)) : AnyLayout(HStackLayout(alignment: .firstTextBaseline, spacing: 8))
        layout {
            VStack(alignment: .leading, spacing: 4) {
                HStack(spacing: 6) {
                    Image(systemName: "bolt.fill").font(.system(size: 12)).foregroundStyle(ActivityLogic.placeLabel(charge) == "Fast" ? Theme.fast : Theme.home)
                    (Text(ActivityLogic.chargeTitle(charge)).foregroundColor(Theme.textPrimary)
                        + Text(ActivityLogic.levelChange(start: charge.startSocPct, end: charge.endSocPct).map { " \($0)" } ?? "").foregroundColor(Theme.textSecondary))
                        .font(Theme.sans(ThemeTokens.typeValue, .regular, relativeTo: .body))
                }
                Text(ActivityLogic.timeRange(start: charge.startedAt, end: charge.endedAt, calendar: calendar)).themeText(.subline)
            }
            if !typeSize.isAccessibilitySize { Spacer(minLength: 8) }
            VStack(alignment: typeSize.isAccessibilitySize ? .leading : .trailing, spacing: 4) {
                Text(ActivityLogic.energyText(charge)).themeText(.value, color: Theme.textPrimary)
                    .underline(charge.isEnergyEstimated, pattern: .dot)
                Text(costText(line)).themeText(.subline, color: costColor(line))
            }
        }
        .themeCard(padding: 3 * Theme.unit)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(ActivityLogic.spoken(charge, calendar: calendar))
    }

    /// An estimate reads "~$x" here (the detail says it is an estimate); the other lines are as the ledger says them.
    private func costText(_ l: ChargeCostLine) -> String {
        l.tone == .estimate ? l.text.replacingOccurrences(of: " (estimate)", with: "") : l.text
    }

    private func costColor(_ l: ChargeCostLine) -> Color {
        switch l.tone {
        case .missing: return Theme.estimate
        case .estimate: return Theme.textBody
        case .free, .billed: return Theme.textPrimary
        }
    }
}
