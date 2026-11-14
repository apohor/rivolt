import SwiftUI

/// The web's Live page for one truck, opened from the Now hero's Live button (design_preview key ios_now_v2): Rivian's picture of the truck
/// and the figures the app already reads live, in sections. Read-only; Back returns to Now.
struct NowLiveView: View {
    let vehicle: Vehicle
    let summary: NowSummary
    var session: LiveSession?
    var liveDrive: LiveDrive?
    var charge: ChargeBrief?
    var images: [VehicleImage]?
    let onBack: () -> Void

    @Environment(\.dynamicTypeSize) private var typeSize
    @Environment(\.colorScheme) private var colorScheme

    private var offline: Bool { summary.presence == .offline }
    /// Streaming only while the truck is in touch: a stale or offline truck shows what it last said, never a live charge or drive.
    private var streaming: Bool { summary.presence == .live && summary.freshness != .paused && summary.freshness != .needsReconnect }

    var body: some View {
        VStack(spacing: 0) {
            ThemeTopBar { AccountMenu(vehicleID: vehicle.id) }
            ScrollView {
                VStack(alignment: .leading, spacing: 16) {
                    backButton
                    VStack(alignment: .leading, spacing: 2) {
                        Text("Live").themeText(.title)
                        Text(streaming ? "Streaming vehicle state from Rivian" : NowCard.footer(for: summary, clock: .now)).themeText(.subline, color: Theme.textSecondary)
                    }
                    .accessibilityElement(children: .combine)
                    truck
                    energy
                    if streaming, summary.kind == .driving || liveDrive != nil { drive }
                    if streaming, comfortRows { climate }
                }
                .padding(4 * Theme.unit)
            }
        }
        .background(ThemePageBackground().ignoresSafeArea())
        .accessibilityIdentifier("now-live-screen")
    }

    private var backButton: some View {
        Button(action: onBack) {
            HStack(spacing: 4) {
                Image(systemName: "chevron.left").font(.footnote.weight(.semibold))
                Text("Now").themeText(.value, color: Theme.textSecondary)
            }
            .frame(minWidth: 44, minHeight: 44, alignment: .leading)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel("Back to Now")
        .accessibilityIdentifier("live-back")
    }

    @ViewBuilder
    private var truck: some View {
        if let url = VehiclePicture.forNow(images, kind: offline ? .unknown : summary.kind, powerKw: summary.chargePowerKw, light: colorScheme == .light) {
            AsyncImage(url: url) { phase in
                if let image = phase.image { image.resizable().scaledToFit().frame(maxWidth: .infinity, maxHeight: 180) }
            }
            .frame(maxWidth: .infinity)
            .accessibilityHidden(true)
        }
    }

    private var energy: some View {
        let detail = ChargingDetail.make(summary: summary, session: session, charge: charge)
        return ThemeSection(title: "\(vehicle.name): \(streaming ? summary.stateLabel : NowCard.pill(for: summary, clock: .now).text)") {
            row("Battery", summary.socPct.map { "\(Int($0.rounded()))%" })
            row("Range", summary.rangeKm.map { km in "\(Int((NowFormat.usesMetric ? km : km / 1.609344).rounded())) \(NowFormat.usesMetric ? "km" : "mi")" })
            row("Charge limit", summary.chargeTargetPct.map { "\(Int($0.rounded()))%" })
            if streaming, let detail {
                row("Power", detail.powerKw.map { "\(NumberText.fixed($0, $0 < 10 ? 1 : 0)) kW" })
                row("Time left", detail.remaining.map { NowFormat.duration($0) })
                row("Energy added", detail.energyKwh.map { "\(NowFormat.oneDecimal($0)) kWh" })
                row("Cost so far", detail.cost.map { NowFormat.costText($0) })
            }
        }
    }

    private var drive: some View {
        ThemeSection(title: "Drive") {
            row("Speed", summary.speedKph.map { NowFormat.speed(mph: $0 / 1.609344) })
            if let d = liveDrive, d.distanceMi > 0 {
                row("Trip so far", NowFormat.distance(mi: d.distanceMi))
                row("Average speed", d.avgSpeedMph > 0 ? NowFormat.speed(mph: d.avgSpeedMph) : nil)
                row("Top speed", d.maxSpeedMph > 0 ? NowFormat.speed(mph: d.maxSpeedMph) : nil)
                row("Efficiency", d.distanceMi >= 1 && d.miPerKwh > 0 ? NowFormat.efficiency(miPerKwh: d.miPerKwh) : nil)
            }
        }
    }

    private var comfortRows: Bool { summary.liveComfort?.cabinValue != nil || summary.outsideTempC != nil }

    private var climate: some View {
        ThemeSection(title: "Climate") {
            row("Cabin", summary.liveComfort?.cabinValue)
            row("Outside", summary.outsideTempC.map { NowComfort.wholeDegrees(celsius: $0) })
        }
    }

    /// A label and its value; a figure the truck has not reported is a dash, never a made-up zero.
    private func row(_ label: String, _ value: String?) -> some View {
        let layout = typeSize.isAccessibilitySize ? AnyLayout(VStackLayout(alignment: .leading, spacing: 2)) : AnyLayout(HStackLayout(alignment: .firstTextBaseline, spacing: 12))
        return layout {
            Text(label).themeText(.value, color: Theme.textSecondary)
            if !typeSize.isAccessibilitySize { Spacer(minLength: 0) }
            Text(value ?? "–").themeText(.value).fixedSize(horizontal: false, vertical: true)
        }
        .accessibilityElement(children: .combine)
    }
}
