import SwiftUI

/// "Your Rivian connection expires Nov 3." with Reconnect, which opens the web's Rivian account settings (the sign-in is done there).
struct RivianExpiryBanner: View {
    let text: String
    let serverURL: URL

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            Text(text).themeText(.value, color: Theme.estimate).frame(maxWidth: .infinity, alignment: .leading)
            if let url = URL(string: RivianExpiryLogic.reconnectPath, relativeTo: serverURL)?.absoluteURL {
                Link("Reconnect", destination: url).font(Theme.sans(ThemeTokens.typeValue, .medium)).foregroundStyle(Theme.accent)
            }
        }
        .padding(.horizontal, 4 * Theme.unit).padding(.vertical, 3 * Theme.unit)
        .background(Theme.estimateFill.opacity(0.08), in: RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous).strokeBorder(Theme.estimateFill.opacity(0.5), lineWidth: 1))
        .accessibilityElement(children: .combine)
    }
}
