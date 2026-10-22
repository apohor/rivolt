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
    @State private var tab = "now"
    @State private var visited: Set<String> = ["now"]

    private var tabs: [ThemeTabItem] {
        [ThemeTabItem(id: "now", label: "Now", icon: "now"), ThemeTabItem(id: "activity", label: "Activity", icon: "activity"),
         ThemeTabItem(id: "money", label: "Money", icon: "money")]
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
    }

    @ViewBuilder
    private func screen(_ id: String) -> some View {
        switch id {
        case "activity": ActivityView()
        case "money": MoneyView()
        default: HomeView()
        }
    }
}
