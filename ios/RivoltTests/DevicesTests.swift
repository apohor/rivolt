import SwiftUI
import XCTest
@testable import Rivolt

private final class DevicesStub: URLProtocol {
    nonisolated(unsafe) static var status = 200
    nonisolated(unsafe) static var body = Data()
    nonisolated(unsafe) static var seen: [URLRequest] = []

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        Self.seen.append(request)
        let resp = HTTPURLResponse(url: request.url!, statusCode: Self.status, httpVersion: nil, headerFields: nil)!
        client?.urlProtocol(self, didReceive: resp, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Self.body)
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}
}

private struct FixedToken: TokenStore {
    func token(for server: URL) throws -> String? { "tok" }
    func setToken(_ token: String, for server: URL) throws {}
    func deleteToken(for server: URL) throws {}
}

private let sessionsJSON = """
{"sessions":[
 {"id":"6f1d2c3a-0000-4000-8000-000000000001","kind":"web","device_label":"Safari on Mac","user_agent":"Mozilla/5.0","created_at":"2026-10-01T10:00:00Z","last_seen_at":"2026-10-24T09:00:00Z","expires_at":"2026-12-01T10:00:00Z","current":false},
 {"id":"6f1d2c3a-0000-4000-8000-000000000002","kind":"app","device_label":"Sam's iPhone","created_at":"2026-10-10T10:00:00Z","last_seen_at":"2026-10-24T12:00:00Z","expires_at":"2026-12-01T10:00:00Z","current":true},
 {"id":"6f1d2c3a-0000-4000-8000-000000000003","kind":"app","device_label":"","created_at":"2026-09-02T10:00:00Z","last_seen_at":"2026-10-20T10:00:00Z","expires_at":"2026-12-01T10:00:00Z","current":false}
]}
"""

final class DevicesClientTests: XCTestCase {
    private func client() -> APIClient {
        let c = URLSessionConfiguration.ephemeral
        c.protocolClasses = [DevicesStub.self]
        return APIClient(baseURL: URL(string: "https://rivolt.dev")!, tokens: FixedToken(), session: URLSession(configuration: c))
    }

    func testTheListIsReadFromTheServersOwnShape() async throws {
        DevicesStub.status = 200
        DevicesStub.body = Data(sessionsJSON.utf8)
        DevicesStub.seen = []
        let list = try await client().signedInDevices()
        XCTAssertEqual(list.count, 3)
        XCTAssertEqual(DevicesStub.seen.first?.httpMethod, "GET")
        XCTAssertEqual(DevicesStub.seen.first?.url?.path, "/api/auth/sessions")
        let me = try XCTUnwrap(list.first { $0.current })
        XCTAssertEqual(me.kind, "app")
        XCTAssertEqual(me.deviceLabel, "Sam's iPhone")
        XCTAssertNotNil(me.lastSeenAt)
    }

    func testSigningOutOneDeviceIsOneDeleteOfThatId() async throws {
        DevicesStub.status = 204
        DevicesStub.body = Data()
        DevicesStub.seen = []
        try await client().signOutDevice(id: "6f1d2c3a-0000-4000-8000-000000000001")
        XCTAssertEqual(DevicesStub.seen.count, 1)
        XCTAssertEqual(DevicesStub.seen.first?.httpMethod, "DELETE")
        XCTAssertEqual(DevicesStub.seen.first?.url?.path, "/api/auth/sessions/6f1d2c3a-0000-4000-8000-000000000001")
    }

    func testThereIsNoSignOutEverywhereElseOnThePhone() throws {
        // The one write the phone has is a single DELETE; nothing in the client posts to revoke-others.
        let src = try String(contentsOfFile: #filePath.replacingOccurrences(of: "RivoltTests/DevicesTests.swift", with: "Rivolt/Net/APIClient.swift"), encoding: .utf8)
        XCTAssertFalse(src.contains("revoke-others"))
    }
}

private final class FakeDevices: DevicesSource, @unchecked Sendable {
    private let lock = NSLock()
    private var _list: [SignedInDevice]
    private var _calls: [String] = []
    private var _fail: Error?
    init(_ list: [SignedInDevice]) { _list = list }
    var calls: [String] { lock.withLock { _calls } }
    var fail: Error? {
        get { lock.withLock { _fail } }
        set { lock.withLock { _fail = newValue } }
    }

    func signedInDevices() async throws -> [SignedInDevice] {
        try lock.withLock {
            _calls.append("list")
            if let _fail { throw _fail }
            return _list
        }
    }

    func signOutDevice(id: String) async throws {
        try lock.withLock {
            _calls.append("delete \(id)")
            if let _fail { throw _fail }
            _list.removeAll { $0.id == id }
        }
    }
}

@MainActor
final class DevicesModelTests: XCTestCase {
    private let t = JSON.parseDate("2026-10-24T12:00:00Z")!
    private func dev(_ id: String, kind: String = "web", label: String? = nil, current: Bool = false, seen: String = "2026-10-24T09:00:00Z") -> SignedInDevice {
        SignedInDevice(id: id, kind: kind, deviceLabel: label, createdAt: JSON.parseDate("2026-10-01T10:00:00Z"), lastSeenAt: JSON.parseDate(seen), current: current)
    }

    func testThisDeviceFirstThenTheMostRecentlySeen() {
        let ordered = DevicesModel.ordered([dev("old", seen: "2026-10-01T00:00:00Z"), dev("me", kind: "app", current: true, seen: "2026-10-01T00:00:00Z"), dev("new", seen: "2026-10-24T11:00:00Z")])
        XCTAssertEqual(ordered.map(\.id), ["me", "new", "old"])
    }

    func testANameIsTheStoredLabelElseTheKind() {
        XCTAssertEqual(DevicesModel.name(dev("a", label: "Safari on Mac")), "Safari on Mac")
        XCTAssertEqual(DevicesModel.name(dev("a", kind: "app", label: "  ")), "iPhone app")
        XCTAssertEqual(DevicesModel.name(dev("a", kind: "web", label: nil)), "Web browser")
    }

    func testTheDetailLeavesOutWhatTheServerDidNotSay() {
        let full = DevicesModel.detail(dev("a"), now: t, calendar: Calendar(identifier: .gregorian))
        XCTAssertTrue(full.hasPrefix("Signed in Oct 1"), full)
        XCTAssertTrue(full.contains("Last active"), full)
        let bare = SignedInDevice(id: "b", kind: "web", deviceLabel: nil, createdAt: nil, lastSeenAt: nil, current: false)
        XCTAssertEqual(DevicesModel.detail(bare, now: t), "")
        let justNow = SignedInDevice(id: "c", kind: "web", deviceLabel: nil, createdAt: nil, lastSeenAt: t.addingTimeInterval(-10), current: false)
        XCTAssertEqual(DevicesModel.detail(justNow, now: t), "Active just now")
    }

    func testSigningOutOneDeviceDeletesItAndReadsTheListAgain() async {
        let src = FakeDevices([dev("me", kind: "app", current: true), dev("other")])
        let m = DevicesModel()
        await m.load(source: src)
        await m.signOut(dev("other"), source: src)
        XCTAssertEqual(src.calls, ["list", "delete other", "list"])
        guard case .loaded(let after) = m.loadState else { return XCTFail("not loaded") }
        XCTAssertEqual(after.map(\.id), ["me"], "what shows is the server's own list")
        XCTAssertNil(m.signOutError)
    }

    func testThisPhoneIsNeverSignedOutFromTheList() async {
        let src = FakeDevices([dev("me", kind: "app", current: true)])
        let m = DevicesModel()
        await m.load(source: src)
        await m.signOut(dev("me", kind: "app", current: true), source: src)
        XCTAssertEqual(src.calls, ["list"], "no delete went out for this phone")
    }

    func testAFailedSignOutSaysSoAndKeepsTheList() async {
        let src = FakeDevices([dev("me", kind: "app", current: true), dev("other")])
        let m = DevicesModel()
        await m.load(source: src)
        src.fail = APIError.server(status: 500, message: "boom")
        await m.signOut(dev("other"), source: src)
        XCTAssertNotNil(m.signOutError)
        XCTAssertFalse(m.signOutError?.contains("boom") ?? true, "the server's text is not shown")
        guard case .loaded(let kept) = m.loadState else { return XCTFail("the list should stay") }
        XCTAssertEqual(kept.count, 2)
    }

    func testAFailedFirstReadSaysWhatWentWrong() async {
        let src = FakeDevices([])
        src.fail = APIError.transport("offline")
        let m = DevicesModel()
        await m.load(source: src)
        guard case .failed(let words) = m.loadState else { return XCTFail("expected a failure") }
        XCTAssertFalse(words.isEmpty)
    }
}

@MainActor
final class DevicesSnapshotTests: XCTestCase {
    func testRenderTheDevicesCard() async throws {
        Theme.registerFonts()
        let now = JSON.parseDate("2026-10-24T12:00:00Z")!
        let list = [SignedInDevice(id: "me", kind: "app", deviceLabel: "Sam's iPhone", createdAt: JSON.parseDate("2026-10-10T10:00:00Z"), lastSeenAt: now.addingTimeInterval(-20), current: true),
                    SignedInDevice(id: "w", kind: "web", deviceLabel: "Safari on Mac", createdAt: JSON.parseDate("2026-10-01T10:00:00Z"), lastSeenAt: now.addingTimeInterval(-3 * 3600), current: false),
                    SignedInDevice(id: "x", kind: "app", deviceLabel: "", createdAt: JSON.parseDate("2026-09-02T10:00:00Z"), lastSeenAt: now.addingTimeInterval(-4 * 86400), current: false)]
        let src = FakeDevices(list)
        let model = DevicesModel()
        await model.load(source: src)
        let view = DevicesContent(model: model, serverURL: URL(string: "https://rivolt.dev")!, onSignOut: { _ in }, onReload: {}, now: now)
            .padding(16).frame(width: 393).background(Theme.background)
        for scheme in [ColorScheme.light, .dark] {
            let r = ImageRenderer(content: view.environment(\.colorScheme, scheme))
            r.scale = 3
            let image = try XCTUnwrap(r.uiImage, "did not render")
            XCTAssertGreaterThan(image.size.height, 100)
            let png = try XCTUnwrap(image.pngData())
            let file = "devices-\(scheme == .dark ? "dark" : "light").png"
            let attachment = XCTAttachment(data: png, uniformTypeIdentifier: "public.png")
            attachment.name = file
            attachment.lifetime = .keepAlways
            add(attachment)
            if let dir = ProcessInfo.processInfo.environment["SNAPSHOT_DIR"], !dir.isEmpty {
                try FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
                try png.write(to: URL(fileURLWithPath: dir).appendingPathComponent(file))
            }
        }
    }
}
