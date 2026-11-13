import SwiftUI

/// The licences of what the app bundles: the Geist and Geist Mono fonts (SIL Open Font License 1.1, `OFL.txt`), the map
/// renderer MapLibre Native (BSD 2-Clause, `Map/LICENSE-MapLibre.txt`), the map style from @protomaps/basemaps (BSD
/// 3-Clause, `Map/LICENSE-protomaps-basemaps.txt`), the map labels' Noto Sans glyphs (SIL OFL 1.1,
/// `Map/glyphs/OFL.txt`) and the map data's attribution (OpenStreetMap, Protomaps). The licence texts ship in the app as
/// the licences require.
struct AcknowledgementsView: View {
    @Environment(\.dismiss) private var dismiss

    /// The charger archive behind a charge's map pin (the server's chargers.pmtiles): the U.S. Department of Energy's
    /// station data (public domain) and Recreation.gov campgrounds (CC BY 4.0, credited as the web does).
    static let chargerData = "Charging stations on a charge's map come from the Alternative Fuels Data Center of the U.S. Department of Energy (NREL AFDC, afdc.energy.gov), public domain. The charger data also holds campgrounds from Recreation.gov (RIDB, catalog.data.gov/dataset/recreation-information-database-ridb) under CC BY 4.0 (creativecommons.org/licenses/by/4.0), filtered by Rivolt."

    /// The web's data credits (web/src/content/credits.md), as Markdown so the licence links work. The outside temperature, the
    /// trip planner's weather and the rain tags are Open-Meteo's, so its credit lives here, not on the Now screen.
    static let weatherCredit = "Weather data by [Open-Meteo.com](https://open-meteo.com/), licensed under [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/). It is shown in the trip planner, on a drive's weather chart and as the outside temperature, and we may have changed it (rounded places, converted units, derived values such as headwind)."
    static let weatherModels = "Open-Meteo's forecasts are built from national weather services' models, including NOAA's GFS and RRFS (US government works) and Environment and Climate Change Canada's GEM (contains information licensed under the [Open Government Licence – Canada](https://open.canada.ca/en/open-government-licence-canada)). Data processed by Open-Meteo.com. Rivolt is not endorsed by Open-Meteo, NOAA or Environment and Climate Change Canada."
    static let radarCredit = "Radar images are from the [NOAA/NWS](https://www.weather.gov/) MRMS base reflectivity service (US government work, public domain). Rivolt shows them as received, only reprojected to map tiles and made partly transparent, and they are not an official NWS product. Rivolt is not endorsed by or affiliated with NOAA or the National Weather Service. Radar is for general awareness, not for safety-critical decisions."
    static let terrainCredit = "The map's shading and the climb along a planned route use Terrain Tiles data (Mapzen, hosted on AWS Open Data). ArcticDEM terrain data DEM(s) were created from DigitalGlobe, Inc., imagery and funded under National Science Foundation awards 1043681, 1559691, and 1542736. Canada terrain data contains information licensed under the Open Government Licence – Canada. Global ETOPO1 terrain data U.S. National Oceanic and Atmospheric Administration. Mexico terrain data source: INEGI, Continental relief, 2016. United States 3DEP (formerly NED) and global GMTED2010 and SRTM terrain data courtesy of the U.S. Geological Survey."

    private func credit(_ markdown: String) -> some View {
        Text((try? AttributedString(markdown: markdown)) ?? AttributedString(markdown))
            .themeText(.value)
            .fixedSize(horizontal: false, vertical: true)
    }

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
        SettingsStack {
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
                ThemeSection(title: "Weather") {
                    credit(Self.weatherCredit)
                    credit(Self.weatherModels).padding(.top, 2 * Theme.unit)
                }
                .padding(.horizontal, 4 * Theme.unit)
                ThemeSection(title: "Weather radar") { credit(Self.radarCredit) }
                    .padding(.horizontal, 4 * Theme.unit)
                    .padding(.top, 4 * Theme.unit)
                ThemeSection(title: "Terrain") { credit(Self.terrainCredit) }
                    .padding(4 * Theme.unit)
                ThemeSection(title: "Charger data") {
                    Text(Self.chargerData)
                        .themeText(.value)
                        .fixedSize(horizontal: false, vertical: true)
                }
                .padding(.horizontal, 4 * Theme.unit)
                .padding(.bottom, 4 * Theme.unit)
            }
            .themePage()
            .navigationTitle("Acknowledgements")
            .navigationBarTitleDisplayMode(.inline)
            .toolbarBackground(Theme.bar, for: .navigationBar)
            .toolbarBackground(.visible, for: .navigationBar)
            .toolbar { ToolbarItem(placement: .confirmationAction) { SheetDoneButton() } }
        }
    }
}
