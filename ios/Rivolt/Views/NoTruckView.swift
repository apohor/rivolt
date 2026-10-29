import SwiftUI

/// What a signed-in account with no truck sees, in place of four empty tabs: what Rivolt needs, the web page where the
/// Rivian is connected (the app never handles Rivian credentials), and the sample truck to look around meanwhile.
enum Onboarding {
    /// Settings, Account, the Rivian account card.
    static func connectURL(server: URL) -> URL? {
        URL(string: "/settings?tab=account#rivian", relativeTo: server)?.absoluteURL
    }

    enum Gate: Equatable { case loading, noTruck, hasTruck }

    /// What the vehicle list says. A failed read is not "no truck": the screens show their own error and retry.
    static func gate(vehicles: [Vehicle]?) -> Gate {
        guard let vehicles else { return .hasTruck }
        return vehicles.isEmpty ? .noTruck : .hasTruck
    }

    static let title = "Connect your Rivian"
    static let explanation = "Rivolt records the drives, charges and costs of your own Rivian, so it needs your Rivian account. Connecting opens rivolt.dev in your browser; the app never sees your Rivian password, and Rivolt stores it nowhere. Rivolt reads your truck's data and sends no commands to it."
    static let sampleCaption = "No Rivian yet, or just curious? The sample truck shows what Rivolt does, with made-up data."
}

/// Decides, once signed in, between the app and the connect screen. While the list is loading it shows a spinner; if the read
/// fails it shows the app, whose screens have their own error and retry.
struct SignedInGate: View {
    @Environment(AppState.self) private var state
    @Environment(\.scenePhase) private var scenePhase
    @State private var gate = Onboarding.Gate.loading

    var body: some View {
        Group {
            switch gate {
            case .loading:
                ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity).background(Theme.background)
            case .noTruck:
                NoTruckView(onCheck: { await check() })
            case .hasTruck:
                AppShell()
            }
        }
        .task { await check() }
        // Back from the web after connecting: look again.
        .onChange(of: scenePhase) { _, new in if new == .active, gate == .noTruck { Task { await check() } } }
    }

    private func check() async {
        let list = try? await state.client.vehicles()
        gate = Onboarding.gate(vehicles: list)
    }
}

struct NoTruckView: View {
    @Environment(AppState.self) private var state
    var onCheck: () async -> Void
    @State private var checking = false

    var body: some View {
        ScrollView {
            NoTruckContent(connect: Onboarding.connectURL(server: state.serverURL), checking: checking,
                           onCheck: { Task { checking = true; await onCheck(); checking = false } },
                           onSample: { state.enterDemoFromAccount() })
        }
        .themePage()
        .safeAreaInset(edge: .top, spacing: 0) { ThemeTopBar { AccountMenu() } }
    }
}

struct NoTruckContent: View {
    var connect: URL?
    var checking = false
    var onCheck: () -> Void
    var onSample: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 4 * Theme.unit) {
            Text(Onboarding.title).font(Theme.sans(ThemeTokens.typeHeadline, .semibold, relativeTo: .title3)).foregroundStyle(Theme.textPrimary)
                .accessibilityAddTraits(.isHeader)
            Text(Onboarding.explanation).themeText(.value, color: Theme.textBody).fixedSize(horizontal: false, vertical: true)
            if let connect {
                Link(destination: connect) {
                    Text("Connect on the web").frame(maxWidth: .infinity)
                }
                .buttonStyle(ThemeButtonStyle())
            }
            Button(action: onCheck) {
                Text(checking ? "Checking…" : "I've connected it, check again").frame(maxWidth: .infinity)
            }
            .buttonStyle(ThemeSecondaryButtonStyle())
            .disabled(checking)
            Divider().overlay(Theme.border).padding(.vertical, Theme.unit)
            Text(Onboarding.sampleCaption).themeText(.subline, color: Theme.textTertiary).fixedSize(horizontal: false, vertical: true)
            Button(action: onSample) {
                Text("See the sample truck").frame(maxWidth: .infinity)
            }
            .buttonStyle(ThemeSecondaryButtonStyle())
        }
        .padding(6 * Theme.unit)
        .frame(maxWidth: .infinity, alignment: .leading)
        .themeCard(padding: 0)
        .padding(4 * Theme.unit)
    }
}
