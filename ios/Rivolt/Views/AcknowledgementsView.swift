import SwiftUI

/// The licences of what the app bundles: for now the Geist and Geist Mono fonts, SIL Open Font License 1.1, whose text
/// ships in the app (`OFL.txt`) as the licence requires.
struct AcknowledgementsView: View {
    @Environment(\.dismiss) private var dismiss

    /// The licence text from the bundle, or a pointer to where it lives if it is somehow missing.
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
