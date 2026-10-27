import SwiftUI

/// What the three charging cards (off-peak, networks, planner defaults) share: the boxed field, the loading and failed states, and
/// the line that says how a save went.
extension View {
    /// A text field's box, as the home-rate field draws it.
    func settingsFieldBox() -> some View {
        self
            .font(Theme.sans(ThemeTokens.typeValue, .regular, relativeTo: .body))
            .foregroundStyle(Theme.textPrimary)
            .padding(.horizontal, 12).frame(minHeight: 44)
            .background(Theme.card, in: RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous).strokeBorder(Theme.borderStrong, lineWidth: 1))
    }
}

/// A label above a control, in the label style.
struct LabeledBox<Content: View>: View {
    let title: String
    @ViewBuilder var content: Content

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(title).themeText(.label)
            content
        }
    }
}

/// Loading, failed (with Try again) and loaded states of a card that reads one thing.
struct CardLoadView<Value: Equatable, Content: View>: View {
    let model: SettingsCardModel<Value>
    /// "the schedule", for "Couldn't load the schedule."
    let what: String
    let onReload: () -> Void
    @ViewBuilder var content: (Value) -> Content

    var body: some View {
        switch model.loadState {
        case .loading:
            HStack(spacing: 8) {
                ProgressView().tint(Theme.textSecondary)
                Text("Loading…").themeText(.value, color: Theme.textSecondary)
            }
        case .failed:
            Text("Couldn't load \(what).").themeText(.value, color: Theme.estimate)
            Button("Try again", action: onReload).buttonStyle(ThemeSecondaryButtonStyle())
        case .loaded(let v):
            content(v)
        }
    }
}

/// How the last save went: saving, saved (with the server's own answer in words), or why not, in plain words. A server that doesn't
/// let the app change this points to the web.
struct SaveStatusView<Value: Equatable>: View {
    let model: SettingsCardModel<Value>
    let savedText: (Value) -> String
    var webURL: URL?
    let onReload: () -> Void

    var body: some View {
        Group {
            switch model.saveState {
            case .idle:
                EmptyView()
            case .saving:
                HStack(spacing: 8) {
                    ProgressView().tint(Theme.textSecondary)
                    Text("Saving…").themeText(.value, color: Theme.textSecondary)
                }
            case .saved(let v):
                Label(savedText(v), systemImage: "checkmark.circle").themeText(.value, color: Theme.textPrimary)
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
        .onChange(of: model.saveState) { _, new in
            switch new {
            case .saved(let v): AccessibilityNotification.Announcement(savedText(v)).post()
            case .failed(let e): AccessibilityNotification.Announcement(e.message).post()
            default: break
            }
        }
    }
}

/// Where the web's charging tab is, for the links that say "on the web".
enum ChargingWebLink {
    static func url(_ serverURL: URL, anchor: String) -> URL? {
        URL(string: "/settings?tab=charging#\(anchor)", relativeTo: serverURL)?.absoluteURL
    }
}
