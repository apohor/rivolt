import SwiftUI

/// The side-view line drawing of the truck shown when there is no Rivian picture or it fails to load: the web's `CarSketch`, from the same
/// art (the R1S and R1T drawings in the asset catalog are generated from `web/src/components/carSketchArt.tsx`, one per theme). R1T for a
/// model of "R1T", R1S for R1S and anything else. While charging, the web's green bolt badge sits over the roofline.
struct CarSketch: View {
    var model: String?
    var charging = false
    @Environment(\.colorScheme) private var colorScheme

    private var isR1T: Bool { model?.trimmingCharacters(in: .whitespaces).uppercased() == "R1T" }

    var body: some View {
        Image("CarSketch\(isR1T ? "R1T" : "R1S")\(colorScheme == .light ? "Light" : "Dark")")
            .resizable()
            .scaledToFit()
            // The badge sits on the drawn image (its fitted size), not on the 220-wide frame the image may not fill.
            .overlay { if charging { bolt } }
            .frame(minWidth: 0, idealWidth: 0, maxWidth: 220)
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(isR1T ? "Rivian R1T" : "Rivian R1S")
            .accessibilityIdentifier("now-car-sketch")
    }

    /// 11% of the width, 22% in from the right and 10% down, as on the web.
    private var bolt: some View {
        GeometryReader { geo in
            let d = geo.size.width * 0.11
            ZStack {
                Circle().fill(Color(red: 0.063, green: 0.725, blue: 0.506)).frame(width: 22, height: 22)
                Path { p in
                    p.move(to: CGPoint(x: 13.5, y: 5.5)); p.addLine(to: CGPoint(x: 8, y: 13)); p.addLine(to: CGPoint(x: 12, y: 13))
                    p.addLine(to: CGPoint(x: 10.5, y: 18.5)); p.addLine(to: CGPoint(x: 16, y: 11)); p.addLine(to: CGPoint(x: 12, y: 11)); p.closeSubpath()
                }
                .fill(Color(red: 0.024, green: 0.031, blue: 0.047))
            }
            .frame(width: 24, height: 24)
            .scaleEffect(d / 24)
            .frame(width: d, height: d)
            .position(x: geo.size.width * (1 - 0.22) - d / 2, y: geo.size.height * 0.10 + d / 2)
        }
        .accessibilityHidden(true)
    }
}

enum PicturePhase { case loading, loaded(Image), failed }

/// A picture loaded from an address, telling its content closure whether it is loading, loaded or failed. `AsyncImage` reports no failure for a
/// bad status or an unreadable body on every path, and the sketch fallback and the Live second try both hang on a failure being seen.
/// The request is the same one `AsyncImage` makes (`URLSession.shared`, the same cache).
struct LoadedPicture<Content: View>: View {
    let url: URL
    let content: (PicturePhase) -> Content
    @State private var phase: PicturePhase = .loading

    init(url: URL, @ViewBuilder content: @escaping (PicturePhase) -> Content) {
        self.url = url
        self.content = content
    }

    var body: some View {
        content(phase)
            .task(id: url) {
                phase = .loading
                do {
                    let (data, response) = try await URLSession.shared.data(from: url)
                    if Task.isCancelled { return }
                    if let http = response as? HTTPURLResponse, !(200..<300).contains(http.statusCode) { phase = .failed; return }
                    if let ui = UIImage(data: data) { phase = .loaded(Image(uiImage: ui)) } else { phase = .failed }
                } catch {
                    // A load cancelled by a new address ends in URLError(.cancelled), not CancellationError: it is no failure.
                    if !Task.isCancelled { phase = .failed }
                }
            }
    }
}
