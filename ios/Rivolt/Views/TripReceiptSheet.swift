import SwiftUI

/// "Share this trip": a saved trip as a receipt image made on the phone from the server's receipt (areas only, rounded near the ends). It makes
/// no link and uploads nothing. The route sketch and the cost are off every time the sheet opens and never remembered.
struct TripReceiptSheet: View {
    let trip: SavedTrip
    let vehicleModel: String
    @Environment(\.dismiss) private var dismiss
    @Environment(AppState.self) private var state

    enum Phase: Equatable {
        case loading
        case ready
        case failed(String)
    }

    @State private var includeSketch = false
    @State private var includeCost = false
    @State private var phase = Phase.loading
    @State private var png: Data?
    /// The receipt as the server cut it, kept per sketch setting so a toggle back and forth shows the same cut.
    @State private var receipts: [Bool: TripReceipt] = [:]
    @State private var label = "Preview of the trip receipt"

    private var cost: TripCost? { TripReceiptLogic.cost(of: trip) }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 16) {
                    switch phase {
                    case .failed(let words):
                        Text(words).themeText(.value, color: Theme.estimate).fixedSize(horizontal: false, vertical: true)
                    case .loading, .ready:
                        Group {
                            if let png, let image = UIImage(data: png) {
                                Image(uiImage: image).resizable().aspectRatio(contentMode: .fit)
                            } else {
                                Theme.shareCardBackground.aspectRatio(1080.0 / 1500.0, contentMode: .fit)
                            }
                        }
                        .clipShape(RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous))
                        .overlay(RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous).strokeBorder(Theme.border, lineWidth: 1))
                        .accessibilityElement()
                        .accessibilityLabel(label)
                        .accessibilityIdentifier("trip-receipt-preview")
                    }
                    toggle("Include route sketch", TripReceiptLogic.sketchSays, $includeSketch)
                    if cost != nil { toggle("Include charging cost", "Adds the estimated charging cost of the trip.", $includeCost) }
                    Text("\(TripReceiptLogic.says) Made on your device. Nothing is uploaded or posted, and no link is made.")
                        .themeText(.subline, color: Theme.textTertiary).fixedSize(horizontal: false, vertical: true)
                    if let png, phase == .ready {
                        ShareLink(item: MoneySharePNG(data: png, name: TripReceiptLogic.fileName),
                                  preview: SharePreview("Trip receipt", image: Image(uiImage: UIImage(data: png) ?? UIImage()))) {
                            Text("Share")
                        }
                        .buttonStyle(ThemeButtonStyle())
                    }
                }
                .padding(4 * Theme.unit)
            }
            .background(ThemePageBackground().ignoresSafeArea())
            .navigationTitle("Share this trip")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } } }
            .task(id: includeSketch) { await load() }
            .onChange(of: includeCost) { _, _ in paint() }
        }
    }

    private func toggle(_ title: String, _ hint: String, _ on: Binding<Bool>) -> some View {
        Toggle(isOn: on) {
            VStack(alignment: .leading, spacing: 2) {
                Text(title).themeText(.value, color: Theme.textPrimary)
                Text(hint).themeText(.subline).fixedSize(horizontal: false, vertical: true)
            }
        }
        .tint(Theme.accentButton)
    }

    private func load() async {
        if receipts[includeSketch] == nil {
            phase = .loading
            do {
                receipts[includeSketch] = try await state.client.tripReceipt(savedID: trip.id, sketch: includeSketch, metric: NowFormat.usesMetric)
            } catch {
                if Task.isCancelled { return }
                phase = .failed(TripReceiptLogic.failure(error))
                return
            }
        }
        paint()
    }

    private func paint() {
        guard let r = receipts[includeSketch] else { return }
        let m = TripReceiptLogic.model(r, vehicle: vehicleModel, metric: NowFormat.usesMetric, includeSketch: includeSketch, includeCost: includeCost, cost: cost)
        png = TripReceiptCanvas.png(m)
        label = "Preview of the trip receipt. " + m.text.replacingOccurrences(of: "\n", with: ". ")
        phase = png == nil ? .failed("Couldn't make the receipt. Try again.") : .ready
    }
}
