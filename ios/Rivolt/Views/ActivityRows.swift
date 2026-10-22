import SwiftUI

private func timeText(_ d: Date, calendar: Calendar) -> String {
    ActivityLogic.timeOfDay(d, calendar: calendar)
}

struct ActivityDriveRow: View {
    let drive: ActivityDrive
    var calendar: Calendar = .current

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 12) {
            Image(systemName: "steeringwheel").foregroundStyle(NowInk.secondary).frame(width: 24)
            VStack(alignment: .leading, spacing: 2) {
                HStack(alignment: .firstTextBaseline) {
                    Text(NowFormat.distance(mi: drive.distanceMi)).font(.headline).monospacedDigit()
                    if let legs = drive.roundTripLegs, legs > 1 {
                        Text("round trip").font(.caption).foregroundStyle(NowInk.secondary)
                    }
                }
                Text(timeText(drive.startedAt, calendar: calendar)).font(.footnote).foregroundStyle(NowInk.secondary)
            }
            Spacer(minLength: 8)
            VStack(alignment: .trailing, spacing: 2) {
                if ActivityLogic.missingEnergy(drive) {
                    Text("No energy reading").font(.footnote).foregroundStyle(NowInk.stale)
                } else if let e = ActivityLogic.efficiency(drive) {
                    Text(NowFormat.efficiency(miPerKwh: e)).font(.subheadline).monospacedDigit()
                }
                if !ActivityLogic.missingEnergy(drive), let c = drive.estimatedCost, c > 0,
                   let m = NowFormat.moneyIfKnown(c, currency: drive.estimatedCurrency) {
                    Text("~\(m) (estimate)").font(.caption).foregroundStyle(NowInk.secondary)
                }
            }
        }
        .padding(.vertical, 4)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(ActivityLogic.spoken(drive, calendar: calendar))
    }
}

struct ActivityChargeRow: View {
    let charge: ActivityCharge
    var calendar: Calendar = .current

    var body: some View {
        let line = ActivityLogic.costLine(charge)
        HStack(alignment: .firstTextBaseline, spacing: 12) {
            Image(systemName: "bolt.fill").foregroundStyle(NowInk.charging).frame(width: 24)
            VStack(alignment: .leading, spacing: 2) {
                HStack(alignment: .firstTextBaseline) {
                    Text("\(NowFormat.oneDecimal(charge.energyAddedKwh)) kWh").font(.headline).monospacedDigit()
                    if let p = ActivityLogic.placeLabel(charge) {
                        Text(p).font(.caption.weight(.medium)).foregroundStyle(NowInk.secondary)
                            .padding(.horizontal, 8).padding(.vertical, 1)
                            .background(.quaternary, in: Capsule())
                    }
                }
                Text(timeText(charge.startedAt, calendar: calendar)).font(.footnote).foregroundStyle(NowInk.secondary)
            }
            Spacer(minLength: 8)
            Text(line.text).font(.subheadline).monospacedDigit()
                .foregroundStyle(line.tone == .missing ? NowInk.stale : line.tone == .estimate ? NowInk.secondary : Color.primary)
                .multilineTextAlignment(.trailing)
        }
        .padding(.vertical, 4)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(ActivityLogic.spoken(charge, calendar: calendar))
    }
}
