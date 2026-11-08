import SwiftUI

/// The (i) beside the charger-type words: a tap opens one plain sentence per type in a popover (never hover-only), on the phone too.
struct ChargerTypeInfoButton: View {
    let kinds: [ChargerKind]
    @State private var open = false

    var body: some View {
        Button { open = true } label: {
            Image(systemName: "info.circle").imageScale(.large).foregroundStyle(Theme.textSecondary).frame(minWidth: 36, minHeight: 36)
        }
        .accessibilityLabel(ChargerLabels.infoLabel)
        .popover(isPresented: $open) {
            ChargerTypeInfoList(kinds: kinds).presentationCompactAdaptation(.popover)
        }
    }
}

struct ChargerTypeInfoList: View {
    let kinds: [ChargerKind]

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            ForEach(kinds, id: \.self) { kind in
                VStack(alignment: .leading, spacing: 2) {
                    Text(ChargerLabels.label(kind)).themeText(.value, color: Theme.textPrimary)
                    Text(ChargerLabels.sentence(kind)).themeText(.subline, color: Theme.textSecondary).fixedSize(horizontal: false, vertical: true)
                }
            }
        }
        .padding(16)
        .frame(width: 300, alignment: .leading)
    }
}
