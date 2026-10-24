import SwiftUI

/// "Signed-in devices": the browsers and iPhones signed in to this account, with a way to sign out one you don't recognise.
/// Reached from the account menu. This phone is marked and has no button here (Sign out in the menu does that, and forgets its
/// token). There is no "sign out everywhere else" on the phone: a stolen phone could lock the owner out, so that stays on the web.
struct DevicesView: View {
    @Environment(AppState.self) private var state
    @Environment(\.dismiss) private var dismiss
    @State private var model = DevicesModel()

    var body: some View {
        NavigationStack {
            ScrollView {
                DevicesContent(model: model, serverURL: state.serverURL,
                               onSignOut: { d in Task { await model.signOut(d, source: state.client) } },
                               onReload: { Task { await model.load(source: state.client) } })
                    .padding(4 * Theme.unit)
            }
            .themePage()
            .navigationTitle("Signed-in devices")
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
struct DevicesContent: View {
    let model: DevicesModel
    let serverURL: URL
    let onSignOut: (SignedInDevice) -> Void
    let onReload: () -> Void
    var now: Date = .now

    @State private var confirming: SignedInDevice?

    private var settingsURL: URL? { URL(string: "/settings", relativeTo: serverURL)?.absoluteURL }

    var body: some View {
        VStack(alignment: .leading, spacing: 4 * Theme.unit) {
            ThemeSection(title: "Where you're signed in") {
                switch model.loadState {
                case .loading:
                    HStack(spacing: 8) {
                        ProgressView().tint(Theme.textSecondary)
                        Text("Loading…").themeText(.value, color: Theme.textSecondary)
                    }
                case .failed(let words):
                    VStack(alignment: .leading, spacing: 12) {
                        Text(words).themeText(.value, color: Theme.estimate).fixedSize(horizontal: false, vertical: true)
                        Button("Try again", action: onReload).buttonStyle(ThemeSecondaryButtonStyle())
                    }
                case .loaded(let devices):
                    VStack(alignment: .leading, spacing: 0) {
                        ForEach(Array(devices.enumerated()), id: \.element.id) { i, d in
                            if i > 0 { Divider().overlay(Theme.border) }
                            row(d).padding(.vertical, 3 * Theme.unit)
                        }
                        if devices.isEmpty {
                            Text("No devices are signed in.").themeText(.value, color: Theme.textSecondary)
                        }
                    }
                    if let error = model.signOutError {
                        Text(error).themeText(.subline, color: Theme.estimate).fixedSize(horizontal: false, vertical: true)
                    }
                }
            }
            footer
        }
        .confirmationDialog(confirming.map { "Sign out \(DevicesModel.name($0))?" } ?? "", isPresented: Binding(get: { confirming != nil }, set: { if !$0 { confirming = nil } }),
                            titleVisibility: .visible) {
            Button("Sign out", role: .destructive) {
                if let d = confirming { onSignOut(d) }
                confirming = nil
            }
            Button("Cancel", role: .cancel) { confirming = nil }
        } message: {
            Text("It will need to sign in again.")
        }
    }

    private func row(_ d: SignedInDevice) -> some View {
        HStack(alignment: .top, spacing: 3 * Theme.unit) {
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 2 * Theme.unit) {
                    Text(DevicesModel.name(d)).themeText(.value, color: Theme.textPrimary).fixedSize(horizontal: false, vertical: true)
                    if d.current { ThemePill(text: d.kind == "app" ? "THIS IPHONE" : "THIS DEVICE", tone: .live) }
                }
                Text(DevicesModel.kindLabel(d)).themeText(.subline, color: Theme.textTertiary)
                let detail = DevicesModel.detail(d, now: now)
                if !detail.isEmpty { Text(detail).themeText(.subline, color: Theme.textSecondary).fixedSize(horizontal: false, vertical: true) }
            }
            Spacer(minLength: 8)
            if !d.current {
                if model.signingOut == d.id {
                    ProgressView().tint(Theme.textSecondary)
                } else {
                    Button("Sign out") { confirming = d }
                        .buttonStyle(ThemeSecondaryButtonStyle())
                        .fixedSize()
                        .disabled(model.signingOut != nil)
                        .accessibilityLabel("Sign out \(DevicesModel.name(d))")
                }
            }
        }
        .accessibilityElement(children: .contain)
    }

    private var footer: some View {
        VStack(alignment: .leading, spacing: 2 * Theme.unit) {
            Text("These are the devices signed in to your Rivolt account. Sign out one you don't recognise.")
                .themeText(.caption).fixedSize(horizontal: false, vertical: true)
            if let url = settingsURL {
                Text("To sign out every other device at once, use Settings on the web.")
                    .themeText(.caption).fixedSize(horizontal: false, vertical: true)
                Link("Open Settings on the web", destination: url).font(Theme.sans(ThemeTokens.typeSubline)).tint(Theme.accent)
            }
        }
        .padding(.horizontal, Theme.unit)
    }
}
