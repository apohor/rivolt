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
    /// Why there is no truck yet, when the server says (nil: it does not, and the older screen stands).
    @State private var discovery: DiscoveryView?

    var body: some View {
        Group {
            if state.termsRequired {
                TermsUpdatedView()
            } else {
            switch gate {
            case .loading:
                ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity).background(Theme.background)
            case .noTruck:
                NoTruckView(discovery: discovery, onCheck: { await check(askRivian: discovery?.kind == .noVehicles) })
            case .hasTruck:
                AppShell()
            }
            }
        }
        .task { await check() }
        .task(id: discovery?.kind == .looking) {
            while discovery?.kind == .looking, !Task.isCancelled {
                try? await Task.sleep(for: Self.lookAgainEvery)
                if Task.isCancelled { return }
                await check()
            }
        }
        // Back from the web after connecting or accepting the Terms: look again.
        .onChange(of: scenePhase) { _, new in
            if new == .active, gate == .noTruck || state.termsRequired { Task { await check() } }
        }
    }

    /// askRivian: the "Check again" tap on "none". The server only looks again about once a day, so the tap asks Rivian now (as the web's Settings
    /// does) before reading the list; a failure there still re-reads. A server that does not hold the account asks the one that does and answers
    /// 503 with Retry-After: that is "checking", so the tap waits the Retry-After, reads, and shows "looking" (with its half-minute re-read) until
    /// the owner's answer lands. The automatic checks never ask Rivian.
    private func check(askRivian: Bool = false) async {
        await state.recheckTerms()
        if state.termsRequired { return }
        var ownerAsked = false
        if askRivian {
            do { _ = try await state.client.refreshRivianVehicles() } catch APIError.server(status: 503, _, let after?) {
                ownerAsked = true
                try? await Task.sleep(for: .seconds(min(max(after, 1), 15)))
            } catch {}
        }
        let list = try? await state.client.vehicles()
        gate = Onboarding.gate(vehicles: list)
        guard gate == .noTruck else { discovery = nil; return }
        var answer: VehicleDiscovery?
        var failure: Error?
        do { answer = try await state.client.vehicleDiscovery() } catch { failure = error }
        let found = VehicleDiscoveryState.from(answer, error: failure)
        // A vehicle turned up since the list was read: look at the list again.
        if found == .found, let again = try? await state.client.vehicles() { gate = Onboarding.gate(vehicles: again) }
        discovery = (found == .empty && ownerAsked ? VehicleDiscoveryState.looking : found)?.view
    }

    /// While the server is still looking, the screen asks again every half minute.
    static let lookAgainEvery: Duration = .seconds(30)
}

struct NoTruckView: View {
    @Environment(AppState.self) private var state
    var discovery: DiscoveryView?
    var onCheck: () async -> Void
    @State private var checking = false

    var body: some View {
        ScrollView {
            NoTruckContent(connect: Onboarding.connectURL(server: state.serverURL), discovery: discovery, checking: checking,
                           onCheck: { Task { checking = true; await onCheck(); checking = false } },
                           onSample: { state.enterDemoFromAccount() })
        }
        .themePage()
        .safeAreaInset(edge: .top, spacing: 0) { ThemeTopBar { AccountMenu() } }
    }
}

struct NoTruckContent: View {
    var connect: URL?
    /// What the server says about finding the vehicles (looking, none, reauth); nil shows the older "Connect your Rivian" screen.
    var discovery: DiscoveryView?
    var checking = false
    var onCheck: () -> Void
    var onSample: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 4 * Theme.unit) {
            Text(discovery?.title ?? Onboarding.title).font(Theme.sans(ThemeTokens.typeHeadline, .semibold, relativeTo: .title3)).foregroundStyle(Theme.textPrimary)
                .accessibilityAddTraits(.isHeader)
            Text(discovery?.body ?? Onboarding.explanation).themeText(.value, color: Theme.textBody).fixedSize(horizontal: false, vertical: true)
            // Looking: nothing to do but wait (and ask again). None: the Rivian settings on the web. Reconnect: that page too.
            if let connect, discovery?.kind != .looking {
                Link(destination: connect) {
                    Text(discovery?.action ?? "Connect on the web").frame(maxWidth: .infinity)
                }
                .buttonStyle(ThemeButtonStyle())
            }
            Button(action: onCheck) {
                Text(checking ? "Checking…" : (discovery == nil ? "I've connected it, check again" : VehicleDiscoveryCopy.check)).frame(maxWidth: .infinity)
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
