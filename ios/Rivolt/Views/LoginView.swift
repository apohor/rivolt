import AuthenticationServices
import SwiftUI

/// Sign-in screen. Rivolt signs in through the server's own web sign-in (the
/// app never asks for a password); the server is rivolt.dev unless the user
/// chooses another under "Other server", which is checked before it's used.
struct LoginView: View {
    @Environment(AppState.self) private var state
    @Environment(\.webAuthenticationSession) private var webAuthenticationSession

    @State private var showOtherServer = false
    @State private var otherServer = ""
    @State private var isWorking = false
    @State private var message: String?

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    Button {
                        Task { await signIn() }
                    } label: {
                        HStack {
                            Spacer()
                            if isWorking {
                                ProgressView().tint(.white)
                            } else {
                                Text("Sign in").fontWeight(.semibold)
                            }
                            Spacer()
                        }
                    }
                    .disabled(isWorking)
                    .listRowBackground(Color.accentColor)
                    .foregroundStyle(.white)
                } footer: {
                    Text("Server: \(state.serverURL.host ?? state.serverURL.absoluteString)")
                }

                if let message {
                    Section {
                        Text(message).font(.footnote)
                    }
                }

                Section {
                    DisclosureGroup("Other server", isExpanded: $showOtherServer) {
                        TextField("https://rivolt.example.com", text: $otherServer)
                            .textContentType(.URL)
                            .keyboardType(.URL)
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled()
                            .submitLabel(.done)
                            .onSubmit { Task { await checkServer() } }
                        Button("Use this server") { Task { await checkServer() } }
                            .disabled(isWorking || otherServer.isEmpty)
                    }
                } footer: {
                    Text("For a self-hosted Rivolt. Rivolt is unofficial and not affiliated with Rivian.")
                }
            }
            .navigationTitle("Rivolt")
        }
    }

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
