import SwiftUI

/// The web's titled card: a header row with the title, a hairline, then the content. The same card surface as
/// `themeCard()`, with no outer padding of its own.
struct ThemeSection<Content: View>: View {
    let title: String
    @ViewBuilder var content: Content

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Text(title)
                .font(Theme.sans(ThemeTokens.typeValue, .medium, relativeTo: .body))
                .foregroundStyle(Theme.textBody)
                .accessibilityAddTraits(.isHeader)
                .padding(.horizontal, 4 * Theme.unit).padding(.vertical, 2.5 * Theme.unit)
                .frame(maxWidth: .infinity, alignment: .leading)
            Rectangle().fill(Theme.border).frame(height: 1)
            VStack(alignment: .leading, spacing: 8) { content }
                .padding(4 * Theme.unit)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        .themeCard(padding: 0)
    }
}
