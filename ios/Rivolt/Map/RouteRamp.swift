import SwiftUI
import UIKit

/// The colours a gradient is drawn in: a short ramp from low to high, as the web's (blue to green to amber for speed), taken from the route-speed
/// tokens so light and dark each get theirs. `t` in 0...1 is where the value sits on the drive's own scale (see `RouteGradient`).
struct RouteRamp {
    let stops: [Color]

    /// Blue, green and amber from the speed bucket tokens (the 5 to 25, 25 to 50 and 50 to 65 mph bands): the web's `speed` ramp.
    static let speed = RouteRamp(stops: [Theme.routeSpeed5to25, Theme.routeSpeed25to50, Theme.routeSpeed50to65])

    /// The colour at `t`, interpolated in sRGB between the two nearest stops, resolved for `traits` (dark or light).
    func color(at t: Double, traits: UITraitCollection) -> UIColor {
        let c = stops.map { $0.rgba(traits) }
        guard c.count >= 2 else { return UIColor(stops.first ?? .clear) }
        let x = min(1, max(0, t.isFinite ? t : 0)) * Double(c.count - 1)
        let i = min(c.count - 2, Int(x.rounded(.down)))
        let f = CGFloat(x - Double(i))
        let a = c[i], b = c[i + 1]
        return UIColor(red: a.r + (b.r - a.r) * f, green: a.g + (b.g - a.g) * f, blue: a.b + (b.b - a.b) * f, alpha: 1)
    }

    /// The same colours as SwiftUI stops, for a legend bar (`n` samples along the ramp).
    func gradient(traits: UITraitCollection, samples n: Int = 12) -> Gradient {
        Gradient(colors: (0...n).map { Color(color(at: Double($0) / Double(n), traits: traits)) })
    }
}

private extension Color {
    func rgba(_ traits: UITraitCollection) -> (r: CGFloat, g: CGFloat, b: CGFloat) {
        var r: CGFloat = 0, g: CGFloat = 0, b: CGFloat = 0, a: CGFloat = 0
        UIColor(self).resolvedColor(with: traits).getRed(&r, green: &g, blue: &b, alpha: &a)
        return (r, g, b)
    }
}
