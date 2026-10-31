import SwiftUI

/// The banner on Now: the first service notice not yet dismissed. Nothing when there is none, in the demo, or when the read fails.
struct ServiceNoticeBanner: View {
    @Environment(AppState.self) private var state
    @State private var notices: [ServiceNotice] = []

    var body: some View {
        Group {
            if let n = NoticeLogic.banner(notices) {
                VStack(alignment: .leading, spacing: 8) {
                    Text(n.title).themeText(.value, color: Theme.textPrimary).accessibilityAddTraits(.isHeader)
                    Text(n.body).themeText(.subline, color: Theme.textBody).fixedSize(horizontal: false, vertical: true)
                    NoticeLinks(links: n.links)
                    Button("Dismiss") { dismiss(n) }.buttonStyle(ThemeSecondaryButtonStyle())
                }
                .padding(4 * Theme.unit)
                .frame(maxWidth: .infinity, alignment: .leading)
                .themeCard(padding: 0)
                .accessibilityElement(children: .contain)
                .accessibilityIdentifier("service-notice-banner")
            }
        }
        .task {
            guard !state.isDemo else { return }
            notices = (try? await state.client.notices()) ?? []
        }
    }

    private func dismiss(_ n: ServiceNotice) {
        notices = notices.map { $0.kind == n.kind ? ServiceNotice(kind: $0.kind, title: $0.title, body: $0.body, links: $0.links, dismissed: true) : $0 }
        // Hidden now; if the write fails the banner is back next time, which is fine.
        Task { try? await state.client.dismissNotice(kind: n.kind) }
    }
}

struct NoticeLinks: View {
    @Environment(AppState.self) private var state
    let links: [ServiceNotice.Link]

    var body: some View {
        let items = links.compactMap { l in NoticeLogic.url(l.href, server: state.serverURL).map { (l.label, $0) } }
        if !items.isEmpty {
            HStack(spacing: 16) {
                ForEach(items, id: \.1) { item in Link(item.0, destination: item.1).font(Theme.sans(ThemeTokens.typeSubline, .medium)).tint(Theme.accent) }
            }
        }
    }
}

/// Settings → Account → Notices: every notice the server still lists, dismissed or not, so the words stay readable.
struct NoticesView: View {
    @Environment(AppState.self) private var state
    @State private var notices: [ServiceNotice] = []

    var body: some View {
        SettingsStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 4 * Theme.unit) {
                    ForEach(notices) { n in
                        VStack(alignment: .leading, spacing: 8) {
                            Text(n.title).themeText(.value, color: Theme.textPrimary)
                            Text(n.body).themeText(.subline, color: Theme.textBody).fixedSize(horizontal: false, vertical: true)
                            NoticeLinks(links: n.links)
                        }
                        .padding(4 * Theme.unit)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .themeCard(padding: 0)
                    }
                }
                .padding(4 * Theme.unit)
            }
            .themePage()
            .navigationTitle("Notices")
            .navigationBarTitleDisplayMode(.inline)
            .toolbarBackground(Theme.bar, for: .navigationBar)
            .toolbarBackground(.visible, for: .navigationBar)
            .toolbar { ToolbarItem(placement: .confirmationAction) { SheetDoneButton() } }
        }
        .task { notices = (try? await state.client.notices()) ?? [] }
    }
}
