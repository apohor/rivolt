import UIKit

/// gap_dim (design preview): a GPS dropout is drawn solid and faded in the colour the route has where the dropout begins, instead of dashed grey.
extension RouteSnap.Drawing {
    /// Opacity and width of a dimmed dropout (the web's 35%, at the track's own width).
    static let gapDimOpacity = 0.35

    /// The colour at each gap's start: the nearest end of a gradient run (its value on the ramp), or of a speed piece (its bucket's colour).
    func gapColors(metric: RouteGradient.Metric, gradientOn: Bool, traits: UITraitCollection) -> [UIColor] {
        func dist2(_ a: RouteSnap.Coord, _ b: RouteSnap.Coord) -> Double {
            let dLat = a.lat - b.lat
            let dLon = (a.lon - b.lon) * cos(a.lat * .pi / 180)
            return dLat * dLat + dLon * dLon
        }
        return gaps.map { gap in
            if gradientOn, let range = range(for: metric) {
                var best: (d: Double, value: Double)?
                for run in runs {
                    let values = run.filledValues(metric)
                    guard let first = values.first, let last = values.last, let a = run.coords.first, let b = run.coords.last else { continue }
                    for (c, v) in [(a, first), (b, last)] {
                        let d = dist2(c, gap.from)
                        if best == nil || d < best!.d { best = (d, v) }
                    }
                }
                guard let v = best?.value else { return UIColor(RouteMapView.color(nil)) }
                let t = range.hi > range.lo ? min(1, max(0, (v - range.lo) / (range.hi - range.lo))) : 0.5
                return RouteRamp.ramp(metric).color(at: t, traits: traits)
            }
            var best: (d: Double, speed: RouteSnap.Speed?)?
            for piece in pieces {
                guard let a = piece.coords.first, let b = piece.coords.last else { continue }
                for c in [a, b] {
                    let d = dist2(c, gap.from)
                    if best == nil || d < best!.d { best = (d, piece.speed) }
                }
            }
            return UIColor(RouteMapView.color(best?.speed))
        }
    }
}
