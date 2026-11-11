// Generated from ios/design/vehicle-discovery.json by ios/design/generate_vehicle_discovery.py. Do not edit by hand:
// change web/src/lib/vehicleDiscovery.json, regenerate vehicle-discovery.json, then run the script. VehicleDiscoveryTests checks the two agree.
import Foundation

/// What the app says while a linked Rivian account has no vehicle on file, one set of words with the web.
enum VehicleDiscoveryCopy {
    static let lookingTitle = "Looking for your vehicles"
    static let lookingBody = "Rivolt is checking your Rivian account. This usually takes a few minutes, and your vehicles show up here when it is done."
    static let noneTitle = "No vehicles on this Rivian account yet"
    static let noneBody = "Your Rivian account is connected, but it lists no vehicle. Rivolt looks again about once a day."
    static let noneAction = "Open Rivian settings"
    static let reauthTitle = "Reconnect your Rivian"
    static let reauthBody = "Rivian needs you to sign in again before Rivolt can look for your vehicles."
    static let reauthAction = "Reconnect Rivian"
    static let check = "Check again"
}
