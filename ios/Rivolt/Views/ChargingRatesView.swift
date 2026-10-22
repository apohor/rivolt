import SwiftUI

/// The home electricity rate, read and changed on the phone: the price per kWh and its currency, used to price charges
/// Rivian reports as free. Reached from the account menu. It sends only what the person changed, shows the server's own
/// answer as the saved rate, and says plainly what went wrong when a save fails. Applying a rate to past charges stays on
/// the web.
struct ChargingRatesView: View {
    @Environment(AppState.self) private var state
    @Environment(\.dismiss) private var dismiss
    @State private var model = ChargingRatesModel()

    var body: some View {
        NavigationStack {
            ScrollView {
                ChargingRatesContent(
                    model: model, serverURL: state.serverURL,
                    onSave: { rate, currency in Task { await model.save(source: state.client, pricePerKwh: rate, currency: currency) } },
                    onReload: { Task { await model.load(source: state.client) } })
                    .padding(4 * Theme.unit)
            }
            .themePage()
            .navigationTitle("Charging rates")
            .navigationBarTitleDisplayMode(.inline)
            .toolbarBackground(Theme.bar, for: .navigationBar)
            .toolbarBackground(.visible, for: .navigationBar)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                        .font(Theme.sans(ThemeTokens.typeValue, .medium))
                        .tint(Theme.accent)
                }
            }
        }
        .task { await model.load(source: state.client) }
    }
}

/// The card and its states (apart from the sheet so a test can render each).
struct ChargingRatesContent: View {
    let model: ChargingRatesModel
    let serverURL: URL
    let onSave: (Double?, String?) -> Void
    let onReload: () -> Void

    @State private var text: String
    @State private var currency: String
    @FocusState private var focused: Bool
    @Environment(\.dynamicTypeSize) private var typeSize

    init(model: ChargingRatesModel, serverURL: URL, onSave: @escaping (Double?, String?) -> Void, onReload: @escaping () -> Void) {
        self.model = model
        self.serverURL = serverURL
        self.onSave = onSave
        self.onReload = onReload
        _text = State(initialValue: model.saved.map { ChargingRatesForm.fieldText($0.homePricePerKwh) } ?? "")
        _currency = State(initialValue: model.saved?.homeCurrency ?? "USD")
    }

    private var webURL: URL? { URL(string: MoneyLink.homeRate.path, relativeTo: serverURL)?.absoluteURL }

    var body: some View {
        ThemeSection(title: "Home electricity rate") {
            VStack(alignment: .leading, spacing: 16) {
                switch model.loadState {
                case .loading:
                    HStack(spacing: 8) {
                        ProgressView().tint(Theme.textSecondary)
                        Text("Loading…").themeText(.value, color: Theme.textSecondary)
                    }
                case .failed:
                    Text("Couldn't load the rate.").themeText(.value, color: Theme.estimate)
                    Button("Try again", action: onReload).buttonStyle(ThemeSecondaryButtonStyle())
                case .loaded(let saved):
                    form(saved)
                }
            }
            .padding(4 * Theme.unit)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .onChange(of: model.saved) { _, new in
            guard let new else { return }
            text = ChargingRatesForm.fieldText(new.homePricePerKwh)
            currency = new.homeCurrency
        }
        .onChange(of: model.saveState) { _, new in
            switch new {
            case .saved(let r): AccessibilityNotification.Announcement("Saved. \(ChargingRatesForm.spokenSummary(r))").post()
            case .failed(let e): AccessibilityNotification.Announcement(e.message).post()
            default: break
            }
        }
    }

    // MARK: The form

    private var parsed: Result<Double, ChargingRatesForm.RateError> { ChargingRatesForm.parse(text) }

    private var currencies: [String] {
        let saved = model.saved?.homeCurrency
        return ChargingRatesForm.currencies + (saved.map { ChargingRatesForm.currencies.contains($0) ? [] : [$0] } ?? [])
    }

    private func patch(_ saved: ChargingRates) -> ChargingRatesPatch? {
        guard case .success(let rate) = parsed else { return nil }
        return ChargingRatesForm.patch(rate: rate, currency: currency, saved: saved)
    }

    @ViewBuilder
    private func form(_ saved: ChargingRates) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text("Saved rate").themeText(.label)
            Text(ChargingRatesForm.summary(saved)).themeText(.value, color: Theme.textPrimary)
                .accessibilityLabel("Saved rate, \(ChargingRatesForm.spokenSummary(saved))")
        }

        let layout = typeSize.isAccessibilitySize ? AnyLayout(VStackLayout(alignment: .leading, spacing: 12)) : AnyLayout(HStackLayout(alignment: .bottom, spacing: 12))
        layout {
            VStack(alignment: .leading, spacing: 4) {
                Text("Price per kWh").themeText(.label)
                TextField("0.15", text: $text)
                    .keyboardType(.decimalPad)
                    .focused($focused)
                    .font(Theme.sans(ThemeTokens.typeValue, .regular, relativeTo: .body))
                    .foregroundStyle(Theme.textPrimary)
                    .padding(.horizontal, 12).frame(minHeight: 44)
                    .background(Theme.card, in: RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous))
                    .overlay(RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous).strokeBorder(Theme.borderStrong, lineWidth: 1))
                    .accessibilityLabel("Price per kilowatt hour")
            }
            VStack(alignment: .leading, spacing: 4) {
                Text("Currency").themeText(.label)
                Picker("Currency", selection: $currency) {
                    ForEach(currencies, id: \.self) { Text($0).tag($0) }
                }
                .pickerStyle(.menu)
                .tint(Theme.textPrimary)
                .frame(minHeight: 44)
            }
        }

        if !text.isEmpty, case .failure(let e) = parsed {
            Text(e.message).themeText(.subline, color: Theme.estimate)
        }
        Text(ChargingRatesForm.explanation).themeText(.caption, color: Theme.textTertiary)

        Button {
            focused = false
            if let p = patch(saved) { onSave(p.homePricePerKwh, p.homeCurrency) }
        } label: {
            Text(model.saveState == .saving ? "Saving…" : "Save").frame(maxWidth: .infinity)
        }
        .buttonStyle(ThemeButtonStyle())
        .disabled(patch(saved) == nil || model.saveState == .saving)

        status
    }

    @ViewBuilder
    private var status: some View {
        switch model.saveState {
        case .idle:
            EmptyView()
        case .saving:
            HStack(spacing: 8) {
                ProgressView().tint(Theme.textSecondary)
                Text("Saving…").themeText(.value, color: Theme.textSecondary)
            }
        case .saved(let r):
            VStack(alignment: .leading, spacing: 4) {
                Label("Saved: \(ChargingRatesForm.summary(r))", systemImage: "checkmark.circle")
                    .themeText(.value, color: Theme.textPrimary)
                    .accessibilityLabel("Saved. \(ChargingRatesForm.spokenSummary(r))")
                Text(ChargingRatesForm.afterSave).themeText(.caption, color: Theme.textTertiary)
            }
        case .failed(let e):
            VStack(alignment: .leading, spacing: 8) {
                Label(e.message, systemImage: "exclamationmark.triangle").themeText(.value, color: Theme.estimate)
                if e == .notAllowed, let u = webURL {
                    Link(destination: u) { Text("Change it on the web ↗").themeText(.subline, color: Theme.accent).fontWeight(.medium) }
                }
                if e == .unknown { Button("Reload", action: onReload).buttonStyle(ThemeSecondaryButtonStyle()) }
            }
        }
    }
}
