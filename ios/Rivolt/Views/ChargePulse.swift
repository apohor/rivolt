import SwiftUI

/// The charging cue on the Now and Live cards (design_preview charge_pulse, as the web's): while power flows (> 0 kW) a small bolt beside the kW
/// figure and the Live dot pulsing softly (about 2 s); plugged in and not charging a still grey plug. Reduced motion keeps the glyph, still. The
/// glyph is hidden from VoiceOver: the status text beside it is the cue, so nothing is announced as it pulses. Key off: no glyph, today's views.
enum ChargePulse {
    enum State: Equatable { case charging, plugged, none }

    /// `.none` when the key is off; else charging above 0 kW, plugged when plugged in and not charging, otherwise `.none`.
    static func state(on: Bool, kw: Double?, plugged: Bool) -> State {
        guard on else { return .none }
        if (kw ?? 0) > 0 { return .charging }
        return plugged ? .plugged : .none
    }

    /// The state for a truck's summary: a charging or plugged truck that is not offline (a stale truck shows no cue).
    static func state(on: Bool, kind: NowSnapshot.State, powerKw: Double?, offline: Bool) -> State {
        guard !offline, kind == .charging || kind == .plugged else { return .none }
        return state(on: on, kw: powerKw, plugged: kind == .plugged)
    }

    /// Emerald-400 on dark, emerald-700 on light (the web's light-theme override), so the glyph keeps 3:1 on the card.
    static func green(_ scheme: ColorScheme) -> Color { scheme == .light ? Color(red: 4 / 255, green: 120 / 255, blue: 87 / 255) : Color(red: 52 / 255, green: 211 / 255, blue: 153 / 255) }
}

/// The bolt (charging) or plug (plugged) beside a figure. Nothing for `.none`.
struct PulseBolt: View {
    let state: ChargePulse.State
    @Environment(\.colorScheme) private var scheme
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var dim = false

    var body: some View {
        switch state {
        case .none: EmptyView()
        case .plugged:
            Image(systemName: "powerplug").font(.footnote.weight(.semibold)).foregroundStyle(Theme.textSecondary)
                .accessibilityHidden(true).accessibilityIdentifier("charge-pulse-bolt-plugged")
        case .charging:
            Image(systemName: "bolt.fill").font(.footnote.weight(.semibold)).foregroundStyle(ChargePulse.green(scheme))
                .opacity(dim ? 0.5 : 1).pulsing($dim, reduceMotion: reduceMotion)
                .accessibilityHidden(true).accessibilityIdentifier("charge-pulse-bolt")
        }
    }
}

/// The Live pill's dot: today's accent dot, pulsing green while charging with the key on.
struct PulseDot: View {
    let pulsing: Bool
    @Environment(\.colorScheme) private var scheme
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var dim = false

    var body: some View {
        Circle().fill(pulsing ? ChargePulse.green(scheme) : Theme.accent).frame(width: 8, height: 8)
            .opacity(pulsing && dim ? 0.5 : 1).pulsing($dim, reduceMotion: reduceMotion, active: pulsing)
            .accessibilityHidden(true)
    }
}

private extension View {
    /// Fades `dim` back and forth (1 s each way, a 2 s cycle); with reduced motion, or when not `active`, nothing moves.
    func pulsing(_ dim: Binding<Bool>, reduceMotion: Bool, active: Bool = true) -> some View {
        onAppear {
            guard active, !reduceMotion else { return }
            withAnimation(.easeInOut(duration: 1).repeatForever(autoreverses: true)) { dim.wrappedValue = true }
        }
        .onDisappear { dim.wrappedValue = false }
    }
}
