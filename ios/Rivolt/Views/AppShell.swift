import SwiftUI

/// Whether the screen is the tab on show. A screen keeps its state when another tab is picked, so it uses this to stop
/// polling while it isn't.
private struct TabActiveKey: EnvironmentKey {
    static let defaultValue = true
}

extension EnvironmentValues {
    var tabActive: Bool {
        get { self[TabActiveKey.self] }
        set { self[TabActiveKey.self] = newValue }
    }
}

/// The logged-in app: the screens and, once there is more than one, the bottom tab bar (the web's). Each screen draws its
/// own top bar, so its actions sit in it. A tab is built when it is first picked and then kept, so going back to it
/// doesn't reload everything.
struct AppShell: View {
    @Environment(AppState.self) private var state
    @Environment(\.scenePhase) private var scenePhase
    @State private var tab = StoreShots.tab
    @State private var visited: Set<String> = [StoreShots.tab]

    /// Whether this server has the trip planner (it answers the saved-trips list; 404 where the flag is off). In the demo, always:
    /// it holds the sample trips.
    @State private var plannerOn = false

    private var tabs: [ThemeTabItem] {
        [ThemeTabItem(id: "now", label: "Now", icon: "now"), ThemeTabItem(id: "activity", label: "Activity", icon: "activity"),
         ThemeTabItem(id: "money", label: "Money", icon: "money")]
            + (plannerOn ? [ThemeTabItem(id: "plan", label: "Plan", icon: "plan")] : [])
            + [ThemeTabItem(id: "truck", label: "Truck", icon: "truck")]
    }

    var body: some View {
        VStack(spacing: 0) {
            ZStack {
                ForEach(tabs) { t in
                    if visited.contains(t.id) {
                        screen(t.id)
                            .opacity(tab == t.id ? 1 : 0)
                            .allowsHitTesting(tab == t.id)
                            .accessibilityHidden(tab != t.id)
                            .environment(\.tabActive, tab == t.id)
                    }
                }
            }
            if tabs.count > 1 { ThemeTabBar(items: tabs, selection: $tab) }
        }
        .background(Theme.background.ignoresSafeArea())
        .onChange(of: tab) { _, new in visited.insert(new) }
        // A tapped notification: show the screen it is about. A saved-trip recap leaves the route for the Plan tab to open the trip;
        // the others are done once the tab is shown. Not in the demo, which has no notifications.
        .onChange(of: state.notifications.pendingRoute, initial: true) { _, route in
            guard let route, !state.isDemo else { return }
            let target = route.tab(available: tabs.map(\.id))
            visited.insert(target)
            tab = target
            if case .trip = route, target == "plan" { return }
            state.notifications.pendingRoute = nil
        }
        .task {
            // The demo shows the sample trips, read only; elsewhere the tab appears where the server has the planner.
            guard !state.isDemo else { plannerOn = true; return }
            plannerOn = (try? await state.client.savedTrips()) != nil
        }
        // Back in front after the account-deletion page: has the account gone? (Nothing happens unless the person went there.)
        .onChange(of: scenePhase) { _, new in if new == .active { Task { await state.recheckAfterDeletion() } } }
    }

    @ViewBuilder
    private func screen(_ id: String) -> some View {
        switch id {
        case "activity": ActivityView()
        case "money": MoneyView()
        case "plan": TripPlanView()
        case "truck": TruckView()
        default: HomeView()
        }
    }
}
