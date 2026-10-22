import SwiftUI

/// The logged-in app: the screens and, once there is more than one, the bottom tab bar (the web's). Each screen draws its
/// own top bar, so its actions sit in it.
struct AppShell: View {
    @State private var tab = "now"
    /// Kept for the signed-in session, so the ledger's 15-minute cache outlives a tab switch.
    @State private var money = MoneyModel()

    private var tabs: [ThemeTabItem] {
        [ThemeTabItem(id: "now", label: "Now", icon: "now"), ThemeTabItem(id: "money", label: "Money", icon: "money")]
    }

    var body: some View {
        VStack(spacing: 0) {
            Group {
                switch tab {
                case "money": MoneyView(model: money)
                default: HomeView()
                }
            }
            if tabs.count > 1 { ThemeTabBar(items: tabs, selection: $tab) }
        }
        .background(Theme.background.ignoresSafeArea())
    }
}
