import SwiftUI

/// The destination of a driving truck as its own block (design_preview key ios_now_tidy): where to, then how far is left and when it arrives as the
/// two figures. The route map goes under it where the screen shows one. The same words as the Live row ("To Work: 12 mi left, arrive 5:40 PM").
struct NowDestinationBlock: View {
    let destination: LiveDestination

    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        let f = destination.figures()
        let layout = typeSize.isAccessibilitySize ? AnyLayout(VStackLayout(alignment: .leading, spacing: 12)) : AnyLayout(HStackLayout(alignment: .top, spacing: 24))
        return VStack(alignment: .leading, spacing: 8) {
            Text("To \(destination.name)").themeText(.value).fixedSize(horizontal: false, vertical: true)
            layout {
                if let left = f.left { figure("Left", left) }
                figure("Arrive", f.arrive)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(destination.line())
        .accessibilityIdentifier("live-destination")
    }

    private func figure(_ label: String, _ value: String) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(label).themeText(.label)
            Text(value).themeText(.title).lineLimit(1).minimumScaleFactor(0.7)
        }
    }
}
