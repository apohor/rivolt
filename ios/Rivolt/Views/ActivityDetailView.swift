import SwiftUI

/// One drive or charge, figures only: no map and no place (DA: nothing here says where).
struct ActivityDetailView: View {
    let item: ActivityItem
    var calendar: Calendar = .current

    var body: some View {
        List {
            switch item {
            case .drive(let d): driveRows(d)
            case .charge(let c): chargeRows(c)
            }
        }
        .navigationTitle(title)
        .navigationBarTitleDisplayMode(.inline)
    }

    private var title: String {
        var f = Date.FormatStyle(timeZone: calendar.timeZone).weekday(.abbreviated).month(.abbreviated).day().hour().minute()
        f.calendar = calendar
        return item.startedAt.formatted(f)
    }

    @ViewBuilder
    private func driveRows(_ d: ActivityDrive) -> some View {
        Section {
            row("Distance", NowFormat.distance(mi: d.distanceMi))
            if let t = ActivityLogic.duration(d) { row("Driving time", NowFormat.duration(t)) }
            if let legs = d.roundTripLegs, legs > 1 { row("Round trip", "\(legs) legs") }
            if d.avgSpeedMph > 0 { row("Average speed", NowFormat.speed(mph: d.avgSpeedMph)) }
            if d.maxSpeedMph > 0 { row("Top speed", NowFormat.speed(mph: d.maxSpeedMph)) }
        }
        Section {
            if ActivityLogic.missingEnergy(d) {
                row("Energy used", "No reading")
            } else {
                if d.energyUsedKwh > 0 { row("Energy used", "\(NowFormat.oneDecimal(d.energyUsedKwh)) kWh") }
                if let e = ActivityLogic.efficiency(d) { row("Efficiency", NowFormat.efficiency(miPerKwh: e)) }
                if let c = d.estimatedCost, c > 0, let m = NowFormat.moneyIfKnown(c, currency: d.estimatedCurrency) {
                    row("Cost", "~\(m) (estimate)")
                }
            }
            if d.startSocPct > 0, d.endSocPct > 0 { row("Charge level", "\(Int(d.startSocPct.rounded()))% to \(Int(d.endSocPct.rounded()))%") }
        }
    }

    @ViewBuilder
    private func chargeRows(_ c: ActivityCharge) -> some View {
        let line = ActivityLogic.costLine(c)
        Section {
            if let p = ActivityLogic.placeLabel(c) { row("Where", p) }
            row("Energy added", "\(NowFormat.oneDecimal(c.energyAddedKwh)) kWh")
            if let t = ActivityLogic.duration(c) { row("Time", NowFormat.duration(t)) }
            if c.maxPowerKw > 0 { row("Peak power", "\(NowFormat.oneDecimal(c.maxPowerKw)) kW") }
            if c.startSocPct > 0, c.endSocPct > 0 { row("Charge level", "\(Int(c.startSocPct.rounded()))% to \(Int(c.endSocPct.rounded()))%") }
            if let n = c.network, !n.isEmpty { row("Network", n) }
        }
        Section {
            row("Cost", line.text)
        } footer: {
            Text(costFooter(line))
        }
    }

    private func costFooter(_ line: ChargeCostLine) -> String {
        switch line.tone {
        case .billed: return "As billed or priced at your own rate."
        case .estimate: return "An estimate, not a bill."
        case .free: return "You marked this charge as free."
        case .missing: return "Set a rate in Settings on the web to price charges like this one."
        }
    }

    private func row(_ label: String, _ value: String) -> some View {
        HStack {
            Text(label).foregroundStyle(NowInk.secondary)
            Spacer(minLength: 12)
            Text(value).monospacedDigit().multilineTextAlignment(.trailing)
        }
        .accessibilityElement(children: .combine)
    }
}
