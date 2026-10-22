import SwiftUI

/// The logged-in app: the screens and, once there is more than one, the bottom tab bar (the web's). Each screen draws its
/// own top bar, so its actions sit in it.
struct AppShell: View {
    @State private var tab = "now"

    private var tabs: [ThemeTabItem] {
        [ThemeTabItem(id: "now", label: "Now", icon: "now")]
    }

    var body: some View {
        VStack(spacing: 0) {
            Group {
                switch tab {
                default: HomeView()
                }
            }
            if tabs.count > 1 { ThemeTabBar(items: tabs, selection: $tab) }
        }
        .background(Theme.background.ignoresSafeArea())
    }
}
