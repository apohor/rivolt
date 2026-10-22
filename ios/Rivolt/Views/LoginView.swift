import AuthenticationServices
import SwiftUI

/// Sign-in screen. Rivolt signs in through the server's own web sign-in (the
/// app never asks for a password); the server is rivolt.dev unless the user
/// chooses another under "Other server", which is checked before it's used.
///
/// Looks like the web's sign-in page (new look): one card on the page, the
/// Rivolt mark, "Sign in" with the beta pill, then the action and the small print.
struct LoginView: View {
    @Environment(AppState.self) private var state
    @Environment(\.webAuthenticationSession) private var webAuthenticationSession

    @State private var showOtherServer = false
    @State private var otherServer = ""
    @State private var isWorking = false
    @State private var message: String?

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 5 * Theme.unit) {
                header
                VStack(alignment: .leading, spacing: 3 * Theme.unit) {
                    signInButton
                    if let reason = unavailable?.errorDescription {
                        Text(reason).themeText(.subline, color: Theme.estimate)
                    }
                    Text("Server: \(state.serverURL.host ?? state.serverURL.absoluteString)")
                        .themeText(.caption)
                    if let message {
                        Text(message).themeText(.subline, color: Theme.textBody)
                    }
                }
                otherServerSection
                footer
            }
            .themeCard(padding: 6 * Theme.unit)
            .padding(.horizontal, 4 * Theme.unit)
            .padding(.vertical, 10 * Theme.unit)
        }
        .scrollDismissesKeyboard(.interactively)
        .themePage()
        .task { await state.refreshSignInStatus() }
    }

    // MARK: Parts

    private var header: some View {
        VStack(alignment: .leading, spacing: 4 * Theme.unit) {
            HStack(spacing: 2.5 * Theme.unit) {
                Image(systemName: "bolt.fill")
                    .font(.system(size: 14, weight: .semibold))
                    .foregroundStyle(Theme.accent)
                    .frame(width: 28, height: 28)
                    .overlay(RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous).strokeBorder(Theme.accent, lineWidth: 1.5))
                    .accessibilityHidden(true)
                Text("Rivolt").font(Theme.sans(20, .semibold)).foregroundStyle(Theme.textPrimary)
            }
            HStack(spacing: 2 * Theme.unit) {
                Text("Sign in").font(Theme.sans(20, .semibold)).foregroundStyle(Theme.textPrimary)
                ThemePill(text: "BETA", tone: .stale)
            }
            .accessibilityElement(children: .combine)
            Text("Your Rivian companion. Signing in opens your browser, with the account you use on the web; the app never sees your password.")
                .themeText(.value, color: Theme.textBody)
                .fixedSize(horizontal: false, vertical: true)
        }
    }

    private var signInButton: some View {
        Button {
            Task { await signIn() }
        } label: {
            ZStack {
                Text("Sign in").opacity(isWorking ? 0 : 1)
                if isWorking { ProgressView().tint(.white) }
            }
            .frame(maxWidth: .infinity)
        }
        .buttonStyle(ThemeButtonStyle())
        // Dimmed when the server can't do app sign-in: no invitation to a tap that can't work.
        .disabled(isWorking || unavailable != nil)
        .opacity(unavailable == nil ? 1 : 0.45)
    }

    private var otherServerSection: some View {
        VStack(alignment: .leading, spacing: 3 * Theme.unit) {
            Button {
                withAnimation(.easeOut(duration: Theme.motion)) { showOtherServer.toggle() }
            } label: {
                HStack(spacing: Theme.unit) {
                    Text("Other server").themeText(.value, color: Theme.textSecondary)
                    Image(systemName: "chevron.right")
                        .font(.system(size: 11, weight: .semibold))
                        .foregroundStyle(Theme.textTertiary)
                        .rotationEffect(.degrees(showOtherServer ? 90 : 0))
                }
            }
            .accessibilityValue(showOtherServer ? "Expanded" : "Collapsed")

            if showOtherServer {
                TextField("https://rivolt.example.com", text: $otherServer)
                    .font(Theme.sans(ThemeTokens.typeValue))
                    .foregroundStyle(Theme.textPrimary)
                    .textContentType(.URL)
                    .keyboardType(.URL)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                    .submitLabel(.done)
                    .onSubmit { Task { await checkServer() } }
                    .padding(.horizontal, 3 * Theme.unit)
                    .padding(.vertical, 2.5 * Theme.unit)
                    .background(Theme.background, in: RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous))
                    .overlay(RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous).strokeBorder(Theme.borderStrong, lineWidth: 1))
                Button("Use this server") { Task { await checkServer() } }
                    .buttonStyle(OutlinedButtonStyle())
                    .disabled(isWorking || otherServer.isEmpty)
                    .opacity(isWorking || otherServer.isEmpty ? 0.45 : 1)
            }
        }
    }

    private var footer: some View {
        VStack(alignment: .leading, spacing: 3 * Theme.unit) {
            Text("For a self-hosted Rivolt, use Other server. Rivolt is unofficial and not affiliated with Rivian.")
                .themeText(.caption)
                .fixedSize(horizontal: false, vertical: true)
            HStack(spacing: 1.5 * Theme.unit) {
                Link("Privacy policy", destination: state.serverURL.appending(path: "privacy"))
                Text("·").accessibilityHidden(true)
                Link("Terms", destination: state.serverURL.appending(path: "terms"))
            }
            .font(Theme.sans(ThemeTokens.typeSubline))
            .foregroundStyle(Theme.textSecondary)
            .underline()
            .frame(maxWidth: .infinity)
        }
    }

    private var unavailable: SignInError? { state.signInStatus.unavailableReason }

    // MARK: Actions

    private func signIn() async {
        message = nil
        isWorking = true
        defer { isWorking = false }
        do {
            try await state.signIn(with: SystemWebAuthenticator(session: webAuthenticationSession))
        } catch {
            message = error.localizedDescription
        }
    }

    private func checkServer() async {
        message = nil
        isWorking = true
        defer { isWorking = false }
        do {
            switch try await state.useServer(otherServer) {
            case .ready, .noAppSignIn:
                message = "Using \(state.serverURL.host ?? "that server")."
                showOtherServer = false
            case .notRivolt:
                message = SignInError.notRivolt.localizedDescription
            case .appTooOld:
                message = SignInError.updateApp.localizedDescription
            }
        } catch {
            message = error.localizedDescription
        }
    }
}

/// The web's secondary button (the sign-in page's "Continue with …"): the card colour, a 1 px border, primary text.
/// TODO: replace with the Theme's secondary style once ux adds one.
private struct OutlinedButtonStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(Theme.sans(ThemeTokens.typeValue, .medium))
            .foregroundStyle(Theme.textPrimary)
            .frame(maxWidth: .infinity)
            .padding(.vertical, 2.5 * Theme.unit)
            .background(Theme.card.opacity(configuration.isPressed ? 1 : 0.8), in: RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous).strokeBorder(Theme.borderStrong, lineWidth: 1))
    }
}
