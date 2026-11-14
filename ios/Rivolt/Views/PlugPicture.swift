import SwiftUI

/// The truck picture's plug state (design_preview plug_anim): a still grey plug badge while plugged in and not charging; while power flows
/// (> 0 kW) a soft green glow pulsing along the base (about 2 s) and a green bolt badge. Reduced motion keeps the glow, still.
enum PlugPicture {
    enum State: Equatable {
        case plugged
        case charging(kw: Double)

        /// What VoiceOver says; the picture itself is otherwise hidden from it.
        var label: String {
            switch self {
            case .plugged: return "Plugged in, not charging"
            case .charging(let kw): return "Charging, \((kw * 10).rounded() / 10) kW".replacingOccurrences(of: ".0 kW", with: " kW")
            }
        }
    }

    /// nil when the flag is off or the truck is not plugged in (a parked, driving, asleep or unknown truck shows no badge).
    static func state(kind: NowSnapshot.State, powerKw: Double?, enabled: Bool) -> State? {
        guard enabled else { return nil }
        switch kind {
        case .charging, .plugged: return (powerKw ?? 0) > 0 ? .charging(kw: powerKw ?? 0) : .plugged
        default: return nil
        }
    }
}

private struct PlugPictureOverlay: ViewModifier {
    let state: PlugPicture.State?
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var pulse = false

    func body(content: Content) -> some View {
        if let state {
            let charging = { if case .charging = state { return true } else { return false } }()
            content
                .overlay(alignment: .bottom) {
                    if charging {
                        Capsule().fill(Color.green.opacity(0.5)).frame(height: 10).padding(.horizontal, 36).blur(radius: 8)
                            .opacity(pulse ? 1 : 0.45)
                            .onAppear {
                                guard !reduceMotion else { pulse = true; return }
                                withAnimation(.easeInOut(duration: 1).repeatForever(autoreverses: true)) { pulse = true }
                            }
                            .onDisappear { pulse = false }
                    }
                }
                .overlay(alignment: .topTrailing) {
                    Image(systemName: charging ? "bolt.fill" : "powerplug")
                        .font(.system(size: 12, weight: .semibold))
                        .foregroundStyle(charging ? Color.green : Color.secondary)
                        .frame(width: 24, height: 24)
                        .background(Circle().fill(charging ? Color.green.opacity(0.2) : Color.gray.opacity(0.25)))
                }
                .accessibilityElement(children: .ignore)
                .accessibilityLabel(state.label)
        } else {
            content.accessibilityHidden(true)
        }
    }
}

extension View {
    func plugPicture(_ state: PlugPicture.State?) -> some View { modifier(PlugPictureOverlay(state: state)) }
}
