import UserNotifications

/// The Notification Service Extension. An APNs payload from Rivolt is only
/// `{id}` with a generic alert, because Apple's push servers are not
/// end-to-end encrypted. This extension runs when it arrives, fetches the real
/// title and body from Rivolt over TLS with the signed-in token (shared through
/// the Keychain access group) and swaps them in. If anything goes wrong, or
/// time runs out, the generic "Rivolt update" is shown as it is.
final class NotificationService: UNNotificationServiceExtension {
    private let lock = NSLock()
    private var contentHandler: ((UNNotificationContent) -> Void)?
    private var content: UNMutableNotificationContent?
    private var task: Task<Void, Never>?

    override func didReceive(_ request: UNNotificationRequest, withContentHandler contentHandler: @escaping (UNNotificationContent) -> Void) {
        let content = (request.content.mutableCopy() as? UNMutableNotificationContent) ?? UNMutableNotificationContent()
        lock.lock()
        self.contentHandler = contentHandler
        self.content = content
        lock.unlock()

        guard let id = NotificationFetch.id(from: request.content.userInfo) else {
            finish()
            return
        }
        task = Task { [weak self] in
            if let text = await NotificationFetch.text(id: id) {
                content.title = text.title
                content.body = text.body
            }
            self?.finish()
        }
    }

    /// iOS is about to kill the extension: deliver what there is.
    override func serviceExtensionTimeWillExpire() {
        task?.cancel()
        finish()
    }

    /// Hands the content over once, whichever of the fetch or the timer gets here first.
    private func finish() {
        lock.lock()
        let handler = contentHandler
        let content = content
        contentHandler = nil
        lock.unlock()
        guard let handler, let content else { return }
        // Never deliver an empty notification: if neither the payload nor the fetch gave words, say the generic one.
        if content.title.isEmpty && content.body.isEmpty {
            content.title = "Rivolt"
            content.body = "Rivolt update"
        }
        handler(content)
    }
}
