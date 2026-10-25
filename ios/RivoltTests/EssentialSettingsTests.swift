import XCTest
@testable import Rivolt

private final class Wire: URLProtocol {
    nonisolated(unsafe) static var status = 200
    nonisolated(unsafe) static var body = "{}"
    nonisolated(unsafe) static var seen: [(method: String, path: String, body: String)] = []
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        var sent = ""
        if let s = request.httpBodyStream {
            s.open(); defer { s.close() }
            var out = Data(); var buf = [UInt8](repeating: 0, count: 1024)
            while s.hasBytesAvailable { let n = s.read(&buf, maxLength: buf.count); if n <= 0 { break }; out.append(buf, count: n) }
            sent = String(data: out, encoding: .utf8) ?? ""
        } else if let d = request.httpBody { sent = String(data: d, encoding: .utf8) ?? "" }
        Self.seen.append((request.httpMethod ?? "", request.url?.path ?? "", sent))
        let resp = HTTPURLResponse(url: request.url!, statusCode: Self.status, httpVersion: nil, headerFields: nil)!
        client?.urlProtocol(self, didReceive: resp, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(Self.body.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

private struct Token: TokenStore {
    func token(for server: URL) throws -> String? { "tok" }
    func setToken(_ token: String, for server: URL) throws {}
    func deleteToken(for server: URL) throws {}
}

/// A source whose answers a test sets, and which records what it was asked.
private final class FakeSource: EssentialSettingsSource, @unchecked Sendable {
    var home = HomeLocation()
    var homeError: Error?
    var matches: [PlaceMatch] = []
    var position = TruckPosition(latitude: 45.52, longitude: -122.68)
    var profile = VehicleProfile()
    private(set) var puts: [HomeLocationBody] = []
    private(set) var restores = 0
    private(set) var searches: [String] = []
    private(set) var saved: [(String, VehicleProfile)] = []

    func homeLocation() async throws -> HomeLocation { home }
    func setHomeLocation(_ body: HomeLocationBody) async throws -> HomeLocation {
        puts.append(body)
        if let homeError { throw homeError }
        home = HomeLocation(set: body.set, latitude: body.latitude ?? 0, longitude: body.longitude ?? 0, label: body.label, address: body.address, source: body.source, previousSet: true, addressSearch: true)
        return home
    }
    func restoreHomeLocation() async throws -> HomeLocation {
        restores += 1
        if let homeError { throw homeError }
        return home
    }
    func searchHomeAddress(_ q: String) async throws -> [PlaceMatch] {
        searches.append(q)
        if let homeError { throw homeError }
        return matches
    }
    func vehicleProfile(id: String) async throws -> VehicleProfile { profile }
    func saveVehicleProfile(id: String, _ p: VehicleProfile) async throws -> VehicleProfile { saved.append((id, p)); if let homeError { throw homeError }; return p }
    func truckPosition(id: String) async throws -> TruckPosition { position }
}

@MainActor
final class EssentialSettingsTests: XCTestCase {
    private func client(_ status: Int = 200, _ body: String = "{}") -> APIClient {
        let c = URLSessionConfiguration.ephemeral
        c.protocolClasses = [Wire.self]
        Wire.status = status; Wire.body = body; Wire.seen = []
        return APIClient(baseURL: URL(string: "https://rivolt.dev")!, tokens: Token(), session: URLSession(configuration: c))
    }

    // MARK: What goes on the wire

    func testSettingHomeSendsOnlyTheFieldsTheServerTakes() async throws {
        let api = client(200, #"{"set":true,"latitude":45.52,"longitude":-122.68,"label":"Home","address":"1 Main St, Springfield","source":"address","previous_set":true,"address_search":true}"#)
        let h = try await api.setHomeLocation(HomeLocationBody(set: true, latitude: 45.52, longitude: -122.68, label: "Home", address: "1 Main St, Springfield", source: "address"))
        let sent = try XCTUnwrap(Wire.seen.first)
        XCTAssertEqual(sent.method, "PUT")
        XCTAssertEqual(sent.path, "/api/settings/home-location")
        let obj = try XCTUnwrap(try JSONSerialization.jsonObject(with: Data(sent.body.utf8)) as? [String: Any])
        XCTAssertEqual(Set(obj.keys), ["set", "latitude", "longitude", "label", "address", "source"], "no read-only fields such as previous_set")
        XCTAssertTrue(h.canRestore)
        XCTAssertTrue(h.canSearchAddress)
        XCTAssertEqual(h.address, "1 Main St, Springfield")
    }

    func testClearingSendsSetFalseAlone() async throws {
        let api = client(200, #"{"set":false,"latitude":0,"longitude":0}"#)
        _ = try await api.setHomeLocation(.clear)
        XCTAssertEqual(Wire.seen.first?.body, #"{"set":false}"#)
    }

    func testRestoreAndSearchPaths() async throws {
        var api = client(200, #"{"set":true,"latitude":1,"longitude":2}"#)
        _ = try await api.restoreHomeLocation()
        XCTAssertEqual(Wire.seen.first?.method, "POST")
        XCTAssertEqual(Wire.seen.first?.path, "/api/settings/home-location/restore")
        api = client(200, #"[{"name":"1 Main St","latitude":45.5,"longitude":-122.6,"admin1":"Oregon","country":"United States"}]"#)
        let found = try await api.searchHomeAddress("1 main st austin")
        XCTAssertEqual(Wire.seen.first?.path, "/api/settings/home-location/search")
        XCTAssertEqual(Wire.seen.first?.body, #"{"q":"1 main st austin"}"#, "the address goes in the body, never the URL")
        XCTAssertEqual(found.first?.line, "1 Main St, Oregon, United States")
    }

    func testTheProfileGoesToTheTrucksOwnPathWithOnlyWhatIsSet() async throws {
        let api = client(200, #"{"tire_type":"winter","wheel_inches":21}"#)
        let p = try await api.saveVehicleProfile(id: "veh-1", VehicleProfile(tireType: "winter", wheelInches: 21, accessories: ["roof_rack"], nativeNacs: false))
        let sent = try XCTUnwrap(Wire.seen.first)
        XCTAssertEqual(sent.method, "PUT")
        XCTAssertEqual(sent.path, "/api/vehicles/veh-1/profile")
        let obj = try XCTUnwrap(try JSONSerialization.jsonObject(with: Data(sent.body.utf8)) as? [String: Any])
        XCTAssertEqual(Set(obj.keys), ["tire_type", "wheel_inches", "accessories", "native_nacs"])
        XCTAssertEqual(p.tireType, "winter")
    }

    // MARK: The home screen's model

    func testSettingAHomeFromAMatchLabelsItHomeAndShowsTheServersAnswer() async {
        let src = FakeSource()
        let m = HomeLocationModel()
        await m.load(source: src)
        await m.setHome(source: src, place: PlaceMatch(name: "1 Main St", latitude: 45.5, longitude: -122.6, admin1: "Oregon", country: "United States"))
        XCTAssertEqual(src.puts.first?.label, "Home")
        XCTAssertEqual(src.puts.first?.source, "address")
        XCTAssertEqual(m.home?.set, true)
        XCTAssertEqual(m.message, "Home set.")
        XCTAssertFalse(m.messageIsError)
    }

    func testTheTruckPositionSetsTheHomeAndNoFixSaysSo() async {
        let src = FakeSource()
        let m = HomeLocationModel()
        await m.setHomeToTruck(source: src, vehicleID: "v")
        XCTAssertEqual(src.puts.first?.latitude, 45.52)
        XCTAssertEqual(src.puts.first?.source, "coordinates")
        // A fix an hour or more old is said so; a never-set fix time (year 1) is no fix.
        let old = FakeSource()
        old.position = TruckPosition(latitude: 45.52, longitude: -122.68, locationFixAt: Date(timeIntervalSince1970: 1_800_000_000 - 7200))
        let mOld = HomeLocationModel()
        await mOld.setHomeToTruck(source: old, vehicleID: "v", now: Date(timeIntervalSince1970: 1_800_000_000))
        XCTAssertTrue(mOld.message?.hasPrefix("Home set to where the truck was last seen") == true, mOld.message ?? "")
        let never = FakeSource()
        never.position = TruckPosition(latitude: 45.52, longitude: -122.68, locationFixAt: Date(timeIntervalSince1970: -62_135_596_800))
        let mNever = HomeLocationModel()
        await mNever.setHomeToTruck(source: never, vehicleID: "v")
        XCTAssertTrue(never.puts.isEmpty, "the zero fix time is no fix, whatever the numbers say")
        let none = FakeSource()
        none.position = TruckPosition(latitude: 0, longitude: 0)
        let m2 = HomeLocationModel()
        await m2.setHomeToTruck(source: none, vehicleID: "v")
        XCTAssertTrue(none.puts.isEmpty, "a position of 0, 0 is no fix: nothing is saved")
        XCTAssertTrue(m2.message?.contains("hasn't reported") == true)
    }

    func testTooManyChangesAndMissingRestoreAreSaidInPlainWords() async {
        let src = FakeSource()
        src.homeError = APIError.server(status: 429, message: "slow down")
        let m = HomeLocationModel()
        await m.clear(source: src)
        XCTAssertEqual(m.message, "That's a lot of changes in a minute. Wait a little, then try again.")
        XCTAssertTrue(m.messageIsError)
        XCTAssertFalse(m.message?.contains("slow down") == true, "never the server's text")
        src.homeError = APIError.server(status: 404, message: "nothing to restore")
        await m.restore(source: src)
        XCTAssertEqual(m.message, "There's no earlier home to go back to.")
    }

    func testAShortAddressIsNotSearchedAndTheTypedAddressIsNotKept() async {
        let src = FakeSource()
        src.matches = [PlaceMatch(name: "1 Main St", latitude: 1, longitude: 2)]
        let m = HomeLocationModel()
        await m.search(source: src, query: "ab")
        XCTAssertTrue(src.searches.isEmpty)
        await m.search(source: src, query: "  1 main st  ")
        XCTAssertEqual(src.searches, ["1 main st"])
        XCTAssertEqual(m.results.count, 1)
        await m.setHome(source: src, place: m.results[0])
        XCTAssertTrue(m.results.isEmpty, "the list of matches goes once a home is chosen")
    }

    // MARK: The profile

    func testSavingTheProfileShowsTheServersAnswer() async {
        let src = FakeSource()
        let m = VehicleProfileModel()
        await m.load(source: src, vehicleID: "v1")
        await m.save(source: src, vehicleID: "v1", VehicleProfile(tireType: "winter", frequentlyTows: true))
        XCTAssertEqual(src.saved.first?.0, "v1")
        XCTAssertEqual(m.saveState, .saved)
        XCTAssertEqual(m.saved?.tireType, "winter")
        src.homeError = APIError.server(status: 403, message: "no")
        await m.save(source: src, vehicleID: "v1", VehicleProfile())
        XCTAssertEqual(m.saveState, .failed("This server doesn't let the app change that. Use the web."))
    }

    func testTheChargePortChoicesMapToTheNacsFlag() {
        XCTAssertNil(VehicleProfileChoices.nacs(forChargePort: ""))
        XCTAssertEqual(VehicleProfileChoices.nacs(forChargePort: "native"), true)
        XCTAssertEqual(VehicleProfileChoices.nacs(forChargePort: "ccs"), false)
        XCTAssertEqual(VehicleProfileChoices.chargePortValue(nil), "")
        XCTAssertEqual(VehicleProfileChoices.chargePortValue(true), "native")
    }

    // MARK: Export and the words

    func testExportIsAHandOffToTheWebDataPage() throws {
        let url = try XCTUnwrap(ExportDataView.url(server: URL(string: "https://rivolt.dev")!))
        XCTAssertEqual(url.absoluteString, "https://rivolt.dev/settings?tab=data")
    }

    func testTheWordsAvoidTheBannedOnes() {
        let words = [EssentialSettingsWords.failure(APIError.server(status: 429, message: "")), EssentialSettingsWords.failure(APIError.server(status: 404, message: ""), restoring: true),
                     EssentialSettingsWords.failure(APIError.server(status: 403, message: "")), VehicleProfileChoices.chargePort.map(\.label).joined(), VehicleProfileChoices.accessories.map(\.label).joined()].joined(separator: " ")
        for banned in ["OK", "Normal", "safe", "secure", "towing", "metered", "exact", "measured"] { XCTAssertFalse(words.contains(banned), banned) }
    }
}
