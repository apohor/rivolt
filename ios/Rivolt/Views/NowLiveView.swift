import SwiftUI

/// The web's Live page for one truck, opened from the Now hero's Live button (design_preview key ios_now_v2): Rivian's picture of the truck
/// and the figures the app already reads live, in sections. Read-only; Back returns to Now.
struct NowLiveView: View {
    let vehicle: Vehicle
    let summary: NowSummary
    var session: LiveSession?
    var liveDrive: LiveDrive?
    /// Where the truck's navigation is going (design_preview key live_destination); nil without a trip or with the design off.
    var destination: LiveDestination?
    var charge: ChargeBrief?
    /// The calmer layout (design_preview key ios_now_tidy): the destination is its own block with the map under it, above the drive figures.
    var tidy = false
    var images: [VehicleImage]?
    /// The gear and closures the picture's view is picked from (the web's door, frunk and gear pick); nil before the state is read.
    var pictureState: VehiclePicture.LiveInputs?
    let onBack: () -> Void

    @Environment(\.dynamicTypeSize) private var typeSize
    @Environment(\.colorScheme) private var colorScheme
    @Environment(AppState.self) private var appState: AppState?

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
                    if tidy, streaming, summary.kind == .driving, let destination, destination.hasFix {
                        ThemeSection(title: "Destination") { NowDestinationBlock(destination: destination) }
                        LiveDestinationMap(destination: destination)
                    }
                    if streaming, summary.kind == .driving || liveDrive != nil || pictureState.map(VehiclePicture.isDriving) == true { drive }
                    if !tidy, streaming, summary.kind == .driving, let destination, destination.hasFix { LiveDestinationMap(destination: destination) }
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
        let urls = VehiclePicture.showsOnLive(kind: summary.kind, state: pictureState, streaming: streaming) ? VehiclePicture.liveCandidates(images, imageURL: vehicle.imageUrl, state: pictureState, light: colorScheme == .light) : []
        // No picture, nothing to hang the plug badge and glow on: the overlay goes with the picture.
        Group {
            if urls.isEmpty { EmptyView() } else {
                LiveTruckPicture(urls: urls)
                    .frame(maxWidth: .infinity)
                    .plugPicture(PlugPicture.state(kind: offline ? .unknown : summary.kind, powerKw: summary.chargePowerKw, enabled: plugAnim))
            }
        }
    }

    private var plugAnim: Bool { appState?.designPreview.contains("plug_anim") == true }

    private var energy: some View {
        let detail = ChargingDetail.make(summary: summary, session: session, charge: charge)
        return ThemeSection(title: "\(vehicle.name): \(streaming ? summary.stateLabel : NowCard.pill(for: summary, clock: .now).text)") {
            row("Battery", summary.socPct.map { "\(Int($0.rounded()))%" })
            row("Range", summary.rangeKm.map { km in "\(Int((NowFormat.usesMetric ? km : km / 1.609344).rounded())) \(NowFormat.usesMetric ? "km" : "mi")" })
            row("Charge limit", summary.chargeTargetPct.map { "\(Int($0.rounded()))%" })
            if streaming, let detail {
                row("Power", detail.powerKw.map { "\(NumberText.fixed($0, $0 < 10 ? 1 : 0)) kW" })
                row("Time left", detail.remaining.map { NowFormat.duration($0) })
                row("Energy added", detail.energyKwh.map { "\(NowFormat.oneDecimal($0)) kWh" }, estimated: detail.energyEstimated)
                row("Cost so far", detail.cost.map { NowFormat.costText($0) })
            }
        }
    }

    private var drive: some View {
        ThemeSection(title: "Drive") {
            if !tidy, streaming, summary.kind == .driving, let destination, destination.hasFix {
                Text(destination.line()).themeText(.value).fixedSize(horizontal: false, vertical: true).accessibilityIdentifier("live-destination")
            }
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
    private func row(_ label: String, _ value: String?, estimated: Bool = false) -> some View {
        let layout = typeSize.isAccessibilitySize ? AnyLayout(VStackLayout(alignment: .leading, spacing: 2)) : AnyLayout(HStackLayout(alignment: .firstTextBaseline, spacing: 12))
        return layout {
            Text(label).themeText(.value, color: Theme.textSecondary)
            if !typeSize.isAccessibilitySize { Spacer(minLength: 0) }
            (Text(value ?? "–") + Text(estimated && value != nil ? " est." : "").font(Theme.sans(ThemeTokens.typeCaption, .regular, relativeTo: .caption)).foregroundColor(Theme.textTertiary))
                .themeText(.value).fixedSize(horizontal: false, vertical: true)
        }
        .accessibilityElement(children: .combine)
        .accessibilityLabel(estimated && value != nil ? "\(label), estimated \(value ?? "")" : "\(label), \(value ?? "none")")
    }
}

/// The Live picture: the first address that loads; one that fails hands over to the next (the vehicle's own `image_url` after the state's pick,
/// as on the web), and with none left nothing is drawn. 160 pt tall at most and 384 wide, the web's `max-h-40 max-w-sm`.
private struct LiveTruckPicture: View {
    let urls: [URL]
    @State private var failed: Set<URL> = []

    var body: some View {
        if let url = urls.first(where: { !failed.contains($0) }) {
            LoadedPicture(url: url) { phase in
                switch phase {
                case .loaded(let image): image.resizable().scaledToFit().frame(maxWidth: 384, maxHeight: 160)
                case .failed: Color.clear.frame(height: 1).onAppear { _ = failed.insert(url) }
                case .loading: Color.clear.frame(height: 1)
                }
            }
            .frame(maxWidth: .infinity)
        }
    }
}
