import SwiftUI

/// The battery as a 240-degree arc, as on the web's `ArcGauge`: a track, a gradient fill to the charge level, a tick at
/// the charge limit and, only while charging, a soft glow. `children` is drawn in the centre.
enum ThemeGaugeTone { case ok, critical, idle }

struct ThemeGauge<Center: View>: View {
    let value: Double          // 0...100
    var limit: Double?         // 0...100: a tick on the arc
    var live = false
    var tone: ThemeGaugeTone = .ok
    var width = CGFloat(ThemeTokens.gaugeSize)
    @ViewBuilder var center: Center

    private let start = 150.0
    private var sweep: Double { ThemeTokens.gaugeSweep }

    private var colors: [Color] {
        switch tone {
        case .ok: return Theme.gaugeOk
        case .critical: return Theme.gaugeCritical
        case .idle: return Theme.gaugeIdle
        }
    }

    var body: some View {
        let scale = width / 120
        let pct = min(max(value, 0), 100)
        ZStack {
            GaugeArc(start: start, sweep: sweep, fraction: 1)
                .stroke(Theme.textBody.opacity(ThemeTokens.gaugeTrackOpacity), style: StrokeStyle(lineWidth: ThemeTokens.gaugeStroke * scale, lineCap: .round))
            GaugeArc(start: start, sweep: sweep, fraction: pct / 100)
                .stroke(LinearGradient(colors: colors, startPoint: .topLeading, endPoint: .bottomTrailing),
                        style: StrokeStyle(lineWidth: ThemeTokens.gaugeStroke * scale, lineCap: .round))
                .shadow(color: live ? Theme.glowGauge : .clear, radius: 5 * scale)
            if let limit, limit > 0, limit <= 100 {
                GaugeTick(start: start, sweep: sweep, fraction: limit / 100)
                    .stroke(Theme.textPrimary, style: StrokeStyle(lineWidth: 2 * scale, lineCap: .butt))
            }
            center.offset(y: -2 * scale)
        }
        .frame(width: 120 * scale, height: 108 * scale)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("Charge level \(Int(pct.rounded())) percent")
        .accessibilityValue(limit.map { "Limit \(Int($0.rounded())) percent" } ?? "")
    }
}

/// The arc in the web's own 120 x 108 box (centre 60, 60; radius 52), scaled to the frame.
private struct GaugeArc: Shape {
    let start: Double
    let sweep: Double
    let fraction: Double

    func path(in rect: CGRect) -> Path {
        let s = rect.width / 120
        var p = Path()
        guard fraction > 0 else { return p }
        p.addArc(center: CGPoint(x: 60 * s, y: 60 * s), radius: CGFloat(ThemeTokens.gaugeRadius) * s,
                 startAngle: .degrees(start), endAngle: .degrees(start + sweep * fraction), clockwise: false)
        return p
    }
}

private struct GaugeTick: Shape {
    let start: Double
    let sweep: Double
    let fraction: Double

    func path(in rect: CGRect) -> Path {
        let s = rect.width / 120
        let a = (start + sweep * fraction) * .pi / 180
        let r = ThemeTokens.gaugeRadius
        var p = Path()
        p.move(to: CGPoint(x: (60 + (r - 8) * cos(a)) * s, y: (60 + (r - 8) * sin(a)) * s))
        p.addLine(to: CGPoint(x: (60 + (r + 7) * cos(a)) * s, y: (60 + (r + 7) * sin(a)) * s))
        return p
    }
}
