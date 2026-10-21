import SwiftUI
import UIKit

/// Text colours that keep 4.5:1 on the card in light mode: the system green and
/// orange are about 2.2:1 on white and the secondary label about 3.4:1. Dark
/// mode keeps the system colours (10:1 on black). The bar and icons keep the
/// bright colours; only words use these.
enum NowInk {
    static let secondary = Color(uiColor: UIColor { trait in
        trait.userInterfaceStyle == .dark ? UIColor.secondaryLabel : UIColor(white: 0.36, alpha: 1)
    })
    static let charging = Color(uiColor: UIColor { trait in
        trait.userInterfaceStyle == .dark ? UIColor.systemGreen : UIColor(red: 27 / 255, green: 122 / 255, blue: 49 / 255, alpha: 1)
    })
    static let stale = Color(uiColor: UIColor { trait in
        trait.userInterfaceStyle == .dark ? UIColor.systemOrange : UIColor(red: 168 / 255, green: 84 / 255, blue: 0, alpha: 1)
    })
}
