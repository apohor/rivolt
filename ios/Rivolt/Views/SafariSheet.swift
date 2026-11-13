import SafariServices
import SwiftUI

/// A web page in an in-app browser sheet (SFSafariViewController), so the person stays in the app and Done brings them back.
struct SafariSheet: UIViewControllerRepresentable {
    let url: URL

    func makeUIViewController(context: Context) -> SFSafariViewController { SFSafariViewController(url: url) }
    func updateUIViewController(_ controller: SFSafariViewController, context: Context) {}
}
