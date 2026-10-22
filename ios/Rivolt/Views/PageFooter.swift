import SwiftUI

/// The web's page footer: the project and legal links, the site's public pages and the app's version. Each link opens
/// the page in Safari: the project's own, or the user's own Rivolt server for Privacy, Terms and the rest.
struct PageFooter: View {
    /// The user's own Rivolt server: the base for Privacy, Terms and the other pages.
    let server: URL

    struct Item: Identifiable, Equatable {
        enum Target: Equatable { case external(String), server(String), text }
        let label: String
        let target: Target
        var id: String { label }
    }

    /// The web footer's links, in its order (the public pages and the sample trips are the site's, not a demo's).
    static let items: [Item] = [
        Item(label: "GitHub", target: .external("https://github.com/apohor/rivolt")),
        Item(label: "MIT licensed", target: .text),
        Item(label: "Docs", target: .external("https://github.com/apohor/rivolt/blob/main/docs/SIGNUP.md")),
        Item(label: "Privacy", target: .server("/privacy")),
        Item(label: "Terms", target: .server("/terms")),
        Item(label: "Lease calculator", target: .server("/lease-calculator")),
        Item(label: "Cost to charge", target: .server("/charging-cost")),
        Item(label: "What's new", target: .server("/changelog")),
        Item(label: "Sample trips", target: .server("/sample-trips")),
        Item(label: "Discord", target: .external("https://discord.gg/FGty2xmdeV")),
    ]

    /// Where a link goes: its own address, or the page on the user's server. Nil for text and for anything that isn't
    /// a plain https (or the server's own) address.
    static func url(for item: Item, server: URL) -> URL? {
        switch item.target {
        case .text: return nil
        case .external(let s):
            guard let u = URL(string: s), u.scheme == "https" else { return nil }
            return u
        case .server(let path):
            return URL(string: path, relativeTo: server)?.absoluteURL
        }
    }

    /// "App v0.1.8 (42)", from the app's own bundle.
    static func versionText(bundle: Bundle = .main) -> String? {
        guard let short = bundle.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String, !short.isEmpty else { return nil }
        if let build = bundle.object(forInfoDictionaryKey: "CFBundleVersion") as? String, !build.isEmpty, build != short {
            return "App v\(short) (\(build))"
        }
        return "App v\(short)"
    }

    var body: some View {
        VStack(spacing: 12) {
            Rectangle().fill(Theme.border).frame(height: 1)
            ThemeFlow(spacing: 8, rowSpacing: 4) {
                ForEach(Array(Self.items.enumerated()), id: \.element.id) { i, item in
                    if i > 0 { dot }
                    entry(item)
                }
                if let v = Self.versionText() {
                    dot
                    Text(v).themeText(.subline, color: Theme.textTertiary)
                }
            }
        }
        .padding(.top, 2 * Theme.unit)
    }

    private var dot: some View {
        Text("·").themeText(.subline, color: Theme.border).accessibilityHidden(true)
    }

    @ViewBuilder
    private func entry(_ item: Item) -> some View {
        if let url = Self.url(for: item, server: server) {
            Link(destination: url) { Text(item.label).themeText(.subline, color: Theme.textTertiary) }
        } else {
            Text(item.label).themeText(.subline, color: Theme.textTertiary)
        }
    }
}
