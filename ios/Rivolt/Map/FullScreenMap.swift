import MapLibre
import SwiftUI

/// A map opened full screen: Done, the title, and the map filling the rest, with a button that puts the camera back on what the inline map showed.
/// Pinch, pan and double-tap zoom; never rotate or tilt (`MLNMapView.displayOnly(interactive:)`). The map itself is the caller's: it is built
/// from the same drawing, legend and pills as the inline one, given the `recenter` count (bump to refit).
struct FullScreenMapScreen<Content: View>: View {
    let title: String
    let recenterLabel: String
    @ViewBuilder var map: (_ recenter: Int) -> Content
    @Environment(\.dismiss) private var dismiss
    @State private var recenter = 0

    var body: some View {
        VStack(spacing: 0) {
            HStack {
                Text(title).font(.headline).foregroundStyle(Theme.textPrimary).lineLimit(1)
                Spacer()
                Button("Done") { dismiss() }
                    .font(.body.weight(.semibold))
                    .frame(minWidth: 44, minHeight: 44)
                    .accessibilityIdentifier("full-screen-map-done")
            }
            .padding(.horizontal, 16)
            .background(Theme.background)
            map(recenter)
                .overlay(alignment: .topTrailing) {
                    Button { recenter += 1 } label: {
                        Image(systemName: "location.north.line")
                            .font(.system(size: 17, weight: .medium))
                            .foregroundStyle(Theme.textPrimary)
                            .frame(width: 44, height: 44)
                            .background(Theme.background.opacity(0.85), in: Circle())
                            .overlay(Circle().strokeBorder(Theme.border, lineWidth: 1))
                    }
                    .padding(8)
                    .accessibilityLabel(recenterLabel)
                }
        }
        .background(Theme.background)
    }
}

/// Makes an inline map open full screen on a tap (the map stays a picture, so the page's scrolling is untouched), with a small expand mark in
/// its corner and an accessibility action "Open map".
struct OpensFullScreenMap<Full: View>: ViewModifier {
    let enabled: Bool
    let title: String
    let recenterLabel: String
    @ViewBuilder var full: (_ recenter: Int) -> Full
    @State private var open = false

    func body(content: Content) -> some View {
        content
            .overlay(alignment: .topTrailing) {
                if enabled {
                    Image(systemName: "arrow.up.left.and.arrow.down.right")
                        .font(.system(size: 12, weight: .semibold))
                        .foregroundStyle(Theme.textSecondary)
                        .frame(width: 28, height: 28)
                        .background(Theme.background.opacity(0.8), in: Circle())
                        .padding(6)
                        .accessibilityHidden(true)
                        .allowsHitTesting(false)
                }
            }
            .contentShape(Rectangle())
            .onTapGesture { if enabled { open = true } }
            .accessibilityAction(named: "Open map") { if enabled { open = true } }
            .fullScreenCover(isPresented: $open) {
                FullScreenMapScreen(title: title, recenterLabel: recenterLabel, map: full)
            }
    }
}

extension View {
    func opensFullScreenMap<Full: View>(enabled: Bool, title: String, recenterLabel: String, @ViewBuilder full: @escaping (_ recenter: Int) -> Full) -> some View {
        modifier(OpensFullScreenMap(enabled: enabled, title: title, recenterLabel: recenterLabel, full: full))
    }
}
