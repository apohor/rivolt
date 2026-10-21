import Foundation

/// The notification kinds a person can switch on or off, as the server stores
/// them per device. The same five as the web's Settings, with the same words.
/// The plug-in reminder and milestones are opt-in: off until turned on.
struct PushPreferences: Codable, Equatable {
    var onChargingDone = true
    var onPlugInReminder = false
    var onAnomaly = true
    var onSoftwareUpdate = true
    var onMilestones = false
    /// The charging Live Activity switch: off until the person turns it on, per iPhone. `chargingLiveKnown`
    /// is false when the server did not send it (a server without Live Activities): the row is then hidden.
    var onChargingLive = false
    var chargingLiveKnown = false

    init() {}

    /// A server that predates a kind leaves it out; it then keeps its default.
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        onChargingDone = try c.decodeIfPresent(Bool.self, forKey: .onChargingDone) ?? true
        onPlugInReminder = try c.decodeIfPresent(Bool.self, forKey: .onPlugInReminder) ?? false
        onAnomaly = try c.decodeIfPresent(Bool.self, forKey: .onAnomaly) ?? true
        onSoftwareUpdate = try c.decodeIfPresent(Bool.self, forKey: .onSoftwareUpdate) ?? true
        onMilestones = try c.decodeIfPresent(Bool.self, forKey: .onMilestones) ?? false
        let live = try c.decodeIfPresent(Bool.self, forKey: .onChargingLive)
        onChargingLive = live ?? false
        chargingLiveKnown = live != nil
    }

    subscript(kind: PushKind) -> Bool {
        get {
            switch kind {
            case .chargingDone: return onChargingDone
            case .plugInReminder: return onPlugInReminder
            case .anomaly: return onAnomaly
            case .softwareUpdate: return onSoftwareUpdate
            case .milestones: return onMilestones
            case .chargingLive: return onChargingLive
            }
        }
        set {
            switch kind {
            case .chargingDone: onChargingDone = newValue
            case .plugInReminder: onPlugInReminder = newValue
            case .anomaly: onAnomaly = newValue
            case .softwareUpdate: onSoftwareUpdate = newValue
            case .milestones: onMilestones = newValue
            case .chargingLive: onChargingLive = newValue
            }
        }
    }
}

enum PushKind: CaseIterable, Identifiable {
    case chargingDone, softwareUpdate, anomaly, plugInReminder, milestones, chargingLive

    var id: Self { self }

    /// The web Settings' wording.
    var title: String {
        switch self {
        case .chargingDone: return "Charging session completes"
        case .softwareUpdate: return "Software updates"
        case .anomaly: return "An anomaly is detected"
        case .plugInReminder: return "Plug-in reminder"
        case .milestones: return "Milestones"
        case .chargingLive: return "Live Activity while charging"
        }
    }

    var help: String? {
        switch self {
        case .plugInReminder:
            return "One notification in the evening if the truck is parked at home, unplugged, and below the level you set on the Rivolt website."
        case .milestones:
            return "A notification when you pass one, like your first 1,000 miles or your 100th charge. Off until you turn it on."
        case .chargingLive:
            return "Shows charge level, power and time to your target on the Lock Screen and Dynamic Island while a charge is running. While this is on, Apple can see your charge level and power during a charge."
        default:
            return nil
        }
    }

    /// The field the server uses, for a one-switch update.
    var wireKey: String {
        switch self {
        case .chargingDone: return "on_charging_done"
        case .softwareUpdate: return "on_software_update"
        case .anomaly: return "on_anomaly"
        case .plugInReminder: return "on_plug_in_reminder"
        case .milestones: return "on_milestones"
        case .chargingLive: return "on_charging_live"
        }
    }
}
