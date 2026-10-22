import SwiftUI

/// The licences of what the app bundles: the Geist and Geist Mono fonts (SIL Open Font License 1.1, `OFL.txt`), the map
/// renderer MapLibre Native (BSD 2-Clause, `Map/LICENSE-MapLibre.txt`), the map style from @protomaps/basemaps (BSD
/// 3-Clause, `Map/LICENSE-protomaps-basemaps.txt`), the map labels' Noto Sans glyphs (SIL OFL 1.1,
/// `Map/glyphs/OFL.txt`) and the map data's attribution (OpenStreetMap, Protomaps). The licence texts ship in the app as
/// the licences require.
struct AcknowledgementsView: View {
    @Environment(\.dismiss) private var dismiss

    /// The licence text from the bundle, or a pointer to where it lives if it is somehow missing.
    /// A licence text bundled under Map/, or a pointer to where it lives.
    static func mapText(_ name: String, in subdirectory: String, bundle: Bundle = .main, fallback: String) -> String {
        guard let url = bundle.url(forResource: name, withExtension: "txt", subdirectory: subdirectory),
              let text = try? String(contentsOf: url, encoding: .utf8) else { return fallback }
        return text
    }

    static func licenceText(bundle: Bundle = .main) -> String {
        guard let url = bundle.url(forResource: "OFL", withExtension: "txt"),
              let text = try? String(contentsOf: url, encoding: .utf8) else {
            return "The Geist licence text is in the app's OFL.txt and at https://openfontlicense.org."
        }
        return text
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                // The web's titled card: the heading in its own row above a hairline, so the paragraph under it
                // doesn't read as the larger text.
                ThemeSection(title: "Geist and Geist Mono") {
                    Text("The app's typeface is Geist, with Geist Mono for the large readouts, from the Geist Project Authors (github.com/vercel/geist-font), used under the SIL Open Font License, Version 1.1.")
                        .themeText(.value)
                        .fixedSize(horizontal: false, vertical: true)
                    Text(Self.licenceText())
                        .themeText(.caption)
                        .textSelection(.enabled)
                        .padding(.top, 2 * Theme.unit)
                }
                .padding(4 * Theme.unit)
                ThemeSection(title: "MapLibre Native") {
                    Text("Maps are drawn with MapLibre Native (github.com/maplibre/maplibre-native), used under the BSD 2-Clause License.")
                        .themeText(.value)
                        .fixedSize(horizontal: false, vertical: true)
                    Text(Self.mapText("LICENSE-MapLibre", in: "Map", fallback: "The licence text is at github.com/maplibre/maplibre-native/blob/main/LICENSE.md."))
                        .themeText(.caption)
                        .textSelection(.enabled)
                        .padding(.top, 2 * Theme.unit)
                }
                .padding(.horizontal, 4 * Theme.unit)
                ThemeSection(title: "Protomaps basemap style") {
                    Text("The map's style comes from @protomaps/basemaps (github.com/protomaps/basemaps), used under the BSD 3-Clause License; its visual design is CC0.")
                        .themeText(.value)
                        .fixedSize(horizontal: false, vertical: true)
                    Text(Self.mapText("LICENSE-protomaps-basemaps", in: "Map", fallback: "The licence text is at github.com/protomaps/basemaps/blob/main/LICENSE.md."))
                        .themeText(.caption)
                        .textSelection(.enabled)
                        .padding(.top, 2 * Theme.unit)
                }
                .padding(.horizontal, 4 * Theme.unit)
                .padding(.top, 4 * Theme.unit)
                ThemeSection(title: "Map labels and data") {
                    Text("Map labels use Noto Sans from the Noto Project Authors, via Protomaps, under the SIL Open Font License, Version 1.1. Map data © OpenStreetMap contributors (openstreetmap.org/copyright), in the Protomaps basemap.")
                        .themeText(.value)
                        .fixedSize(horizontal: false, vertical: true)
                    Text(Self.mapText("OFL", in: "Map/glyphs", fallback: "The licence text is at openfontlicense.org."))
                        .themeText(.caption)
                        .textSelection(.enabled)
                        .padding(.top, 2 * Theme.unit)
                }
                .padding(4 * Theme.unit)
            }
            .themePage()
            .navigationTitle("Acknowledgements")
            .navigationBarTitleDisplayMode(.inline)
            .toolbarBackground(Theme.bar, for: .navigationBar)
            .toolbarBackground(.visible, for: .navigationBar)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                        .font(Theme.sans(ThemeTokens.typeValue, .medium))
                        .tint(Theme.accent)
                }
            }
        }
    }
}
