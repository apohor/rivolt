import XCTest
@testable import Rivolt

/// web/src/lib/chargerNetwork.test.ts and poi.ts's rules, case for case.
final class ChargerPinTests: XCTestCase {
    private typealias Node = ChargerPin.Node
    private typealias Choice = ChargerPin.Choice

    private func node(_ name: String? = nil, operatorName: String? = nil, network: String? = nil, brand: String? = nil, kw: Double? = nil) -> Node {
        Node(lat: 0, lon: 0, name: name, operatorName: operatorName, network: network, brand: brand, maxPowerKW: kw)
    }

    private var tesla: Node { node("Tesla Supercharger", operatorName: "Tesla, Inc.", brand: "Tesla") }

    func testNetworkKeyNormalisesTheNetworksWeSee() {
        XCTAssertEqual(ChargerPin.networkKey("Tesla Supercharger"), .tesla)
        XCTAssertEqual(ChargerPin.networkKey("Rivian Adventure Network"), .rivian)
        XCTAssertEqual(ChargerPin.networkKey("RAN"), .rivian)
        XCTAssertEqual(ChargerPin.networkKey("Electrify America"), .electrifyAmerica)
        XCTAssertEqual(ChargerPin.networkKey("EA"), .electrifyAmerica)
        XCTAssertNil(ChargerPin.networkKey(nil, ""))
        XCTAssertNil(ChargerPin.networkKey("Some Local Charger"))
    }

    func testTheSnapStaysWhenTheChargerAgreesWithTheLedger() {
        XCTAssertEqual(ChargerPin.choose(node("Electrify America", network: "Electrify America"), known: "Electrify America", isDC: true),
                       Choice(snap: true, label: "Electrify America"))
    }

    /// The ledger's network wins: a different network's node is never named.
    func testTheLedgerNetworkWinsOverADifferentNode() {
        XCTAssertEqual(ChargerPin.choose(tesla, known: "Rivian Adventure Network", isDC: true), Choice(snap: false, label: "Rivian Adventure Network"))
        XCTAssertEqual(ChargerPin.choose(tesla, known: "Electrify America", isDC: true), Choice(snap: false, label: "Electrify America"))
        XCTAssertEqual(ChargerPin.choose(tesla, known: "RAN", isDC: true), Choice(snap: false, label: "Rivian Adventure Network"))
        XCTAssertEqual(ChargerPin.choose(tesla, known: "Tesla", isDC: true), Choice(snap: true, label: "Tesla Supercharger"))
        XCTAssertEqual(ChargerPin.choose(node("Charging station"), known: "Rivian Adventure Network", isDC: true),
                       Choice(snap: false, label: "Rivian Adventure Network"))
        XCTAssertEqual(ChargerPin.displayNetwork("  Blink  "), "Blink")
        XCTAssertEqual(ChargerPin.choose(tesla, known: "Blink", isDC: true), Choice(snap: false, label: "Blink"))
    }

    func testAnUnknownNetworkKeepsTheSnapOrSaysSomethingGeneric() {
        XCTAssertEqual(ChargerPin.choose(tesla, known: nil, isDC: true), Choice(snap: true, label: "Tesla Supercharger"))
        XCTAssertEqual(ChargerPin.choose(node("Joe's Chargers"), known: "unknown", isDC: true), Choice(snap: true, label: "Joe's Chargers"))
        XCTAssertEqual(ChargerPin.choose(nil, known: nil, isDC: true), Choice(snap: false, label: "DC fast charger"))
        XCTAssertEqual(ChargerPin.choose(nil, known: nil, isDC: false), Choice(snap: false, label: "Charging station"))
        XCTAssertEqual(ChargerPin.choose(nil, known: nil), Choice(snap: false, label: "Charging station"))
        XCTAssertEqual(ChargerPin.choose(nil, known: "EVgo", isDC: true), Choice(snap: false, label: "EVgo"))
    }

    /// A session that isn't DC never snaps to a DC-only node.
    func testANonDCSessionNeverSnapsToADCOnlyNode() {
        XCTAssertEqual(ChargerPin.choose(tesla, known: nil, isDC: false), Choice(snap: false, label: "Charging station"))
        XCTAssertEqual(ChargerPin.choose(tesla, known: "unknown", isDC: false), Choice(snap: false, label: "Charging station"))
        XCTAssertEqual(ChargerPin.choose(tesla, known: "ChargePoint", isDC: false), Choice(snap: false, label: "ChargePoint"))
        XCTAssertEqual(ChargerPin.choose(node("Tesla Supercharger"), known: nil, isDC: false), Choice(snap: false, label: "Charging station"))
        XCTAssertEqual(ChargerPin.choose(node("Fast", kw: 150), known: nil, isDC: false), Choice(snap: false, label: "Charging station"))
        // AC or untyped nodes are fine.
        XCTAssertEqual(ChargerPin.choose(node("Hotel EV parking", operatorName: "ChargePoint", kw: 7), known: nil, isDC: false),
                       Choice(snap: true, label: "Hotel EV parking"))
        XCTAssertEqual(ChargerPin.choose(node("Hotel"), known: nil, isDC: false), Choice(snap: true, label: "Hotel"))
    }

    func testTheACSubBrandsKeepTheirOwnNames() {
        let dest = node("Tesla Destination", operatorName: "Tesla, Inc.", brand: "Tesla")
        XCTAssertEqual(ChargerPin.choose(dest, known: nil, isDC: false), Choice(snap: true, label: "Tesla Destination"))
        XCTAssertEqual(ChargerPin.choose(dest, known: "Tesla Destination", isDC: false), Choice(snap: true, label: "Tesla Destination"))
        XCTAssertEqual(ChargerPin.displayNetwork("Rivian Waypoints"), "Rivian Waypoints")
        XCTAssertEqual(ChargerPin.networkKey("Rivian Waypoints"), .rivianWaypoints)
        XCTAssertEqual(ChargerPin.choose(node("Rivian Waypoints"), known: nil, isDC: false), Choice(snap: true, label: "Rivian Waypoints"))
        XCTAssertEqual(ChargerPin.choose(node("Rivian Waypoints"), known: "Rivian Adventure Network", isDC: true),
                       Choice(snap: false, label: "Rivian Adventure Network"))
    }

    /// ChargeMap's own step before choosePin: neither a charger nor a network is the plain pin, without words.
    func testThePinTheMapDraws() {
        XCTAssertEqual(ChargerPin.pin(snapped: nil, network: nil, isDC: true), .plain)
        XCTAssertEqual(ChargerPin.pin(snapped: nil, network: "  ", isDC: true), .plain)
        XCTAssertEqual(ChargerPin.pin(snapped: nil, network: "EVgo", isDC: true), .own(label: "EVgo"))
        XCTAssertEqual(ChargerPin.pin(snapped: tesla, network: "Rivian Adventure Network", isDC: true), .own(label: "Rivian Adventure Network"))
        XCTAssertEqual(ChargerPin.pin(snapped: tesla, network: nil, isDC: true), .snapped(tesla, label: "Tesla Supercharger"))
        XCTAssertNil(ChargerPin.Pin.plain.label)
    }

    func testTheNearestChargerWithinTheRadiusSkipsCampgrounds() {
        let lat = 32.7767, lon = -96.7970
        let cands = [
            ChargerPin.Candidate(lat: lat + 0.001, lon: lon, props: ["name": "Campground", "facility_type": "CAMPGROUND", "socket:nema_14_50": "2"]),
            ChargerPin.Candidate(lat: lat + 0.002, lon: lon, props: ["name": "Fast", "network": "EVgo", "socket:ccs:output": "150 kW", "charging:output": "50000 W"]),
            ChargerPin.Candidate(lat: lat + 0.02, lon: lon, props: ["name": "Too far"]),
        ]
        let n = ChargerPin.nearest(cands, lat: lat, lon: lon, archive: true)
        XCTAssertEqual(n?.name, "Fast")
        XCTAssertEqual(n?.network, "EVgo")
        XCTAssertEqual(n?.maxPowerKW, 150)
        XCTAssertEqual(n?.distanceM ?? 0, 222, accuracy: 2)
        // A campground with its own charger stays a charger.
        XCTAssertFalse(ChargerPin.isCampgroundOnly(["ridb_id": "123", "l2_count": "2"]))
        XCTAssertFalse(ChargerPin.isCampgroundOnly(["facility_type": "RV_PARK", "socket:type1": "1"]))
        XCTAssertTrue(ChargerPin.isCampgroundOnly(["hookup_amps": 50, "socket:nema_14_50": "yes"]))
        XCTAssertNil(ChargerPin.nearest(Array(cands.suffix(1)), lat: lat, lon: lon, archive: true))
        // The basemap's pois: only charging stations, only their names.
        let pois = [
            ChargerPin.Candidate(lat: lat, lon: lon + 0.0005, props: ["kind": "cafe", "name": "Coffee"]),
            ChargerPin.Candidate(lat: lat, lon: lon + 0.001, props: ["kind": "charging_station", "name": "Lot chargers", "network": "Tesla"]),
        ]
        let p = ChargerPin.nearest(pois, lat: lat, lon: lon, archive: false)
        XCTAssertEqual(p?.name, "Lot chargers")
        XCTAssertNil(p?.network)
    }

    func testPowerStrings() {
        XCTAssertEqual(ChargerPin.powerKW("150 kW"), 150)
        XCTAssertEqual(ChargerPin.powerKW("22kW"), 22)
        XCTAssertEqual(ChargerPin.powerKW("11,000 W"), 11)
        XCTAssertNil(ChargerPin.powerKW("fast"))
        XCTAssertNil(ChargerPin.powerKW(7))
    }

    func testTheChargeLocationDecodesOnlyItsFourFields() throws {
        let json = #"{"ID":"c1","VehicleID":"veh-fixture","Lat":32.7767,"Lon":-96.797,"MaxPowerKW":148.2,"network":"Electrify America","Cost":12.5,"EnergyAddedKWh":40}"#
        let c = try JSON.decoder().decode(ChargeLocation.self, from: Data(json.utf8))
        XCTAssertEqual(c, ChargeLocation(lat: 32.7767, lon: -96.797, maxPowerKW: 148.2, network: "Electrify America"))
        XCTAssertTrue(c.isDC)
        XCTAssertTrue(c.hasFix)
        XCTAssertEqual(Mirror(reflecting: c).children.compactMap(\.label), ["lat", "lon", "maxPowerKW", "network"])
        let none = try JSON.decoder().decode(ChargeLocation.self, from: Data(#"{"Lat":0,"Lon":0,"MaxPowerKW":7}"#.utf8))
        XCTAssertFalse(none.hasFix)
        XCTAssertFalse(none.isDC)
        XCTAssertNil(none.network)
    }
}
