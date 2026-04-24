import SwiftUI

/// Username + password + server URL. Three fields is the whole login
/// surface: Rivolt is self-hosted, so the "which server" question is
/// just as user-facing as the credential itself. Pre-filled from
/// `AppState.serverURLString` (which seeds from Info.plist), so most
/// users never touch it.
struct LoginView: View {
    @Environment(AppState.self) private var state

    @State private var username = ""
    @State private var password = ""
    @State private var server = ""
    @State private var isSubmitting = false
    @State private var errorMessage: String?
    @FocusState private var focused: Field?

    private enum Field { case server, username, password }

    var body: some View {
        NavigationStack {
            Form {
                Section("Server") {
                    TextField("https://rivolt.example.com", text: $server)
                        .textContentType(.URL)
                        .keyboardType(.URL)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .focused($focused, equals: .server)
                        .submitLabel(.next)
                        .onSubmit { focused = .username }
                }
                Section("Account") {
                    TextField("Username", text: $username)
                        .textContentType(.username)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .focused($focused, equals: .username)
                        .submitLabel(.next)
                        .onSubmit { focused = .password }
                    SecureField("Password", text: $password)
                        .textContentType(.password)
                        .focused($focused, equals: .password)
                        .submitLabel(.go)
                        .onSubmit { Task { await submit() } }
                }
                if let errorMessage {
                    Section {
                        Text(errorMessage)
                            .foregroundStyle(.red)
                            .font(.footnote)
                    }
                }
                Section {
                    Button {
                        Task { await submit() }
                    } label: {
                        HStack {
                            Spacer()
                            if isSubmitting {
                                ProgressView().tint(.white)
                            } else {
                                Text("Sign in")
                                    .fontWeight(.semibold)
                            }
                            Spacer()
                        }
                    }
                    .disabled(isSubmitting || username.isEmpty || password.isEmpty || server.isEmpty)
                    .listRowBackground(Color.accentColor)
                    .foregroundStyle(.white)
                }
            }
            .navigationTitle("Rivolt")
        }
        .onAppear {
            if server.isEmpty { server = state.serverURLString }
            if focused == nil { focused = .username }
        }
    }

    private func submit() async {
        errorMessage = nil
        isSubmitting = true
        defer { isSubmitting = false }
        // Persist the server URL before hitting login — that way a
        // failed login against a typo-URL still leaves the corrected
        // value stored for the next attempt.
        if server != state.serverURLString { state.serverURLString = server }
        do {
            try await state.login(username: username, password: password)
        } catch {
            errorMessage = error.localizedDescription
        }
    }
}
