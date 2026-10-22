// swift-tools-version:5.9
// MapLibre Native for iOS (BSD-2-Clause), the drive and charging maps' renderer. The prebuilt framework is pinned by
// URL and SHA-256 here, in this repository: SwiftPM refuses a download whose checksum differs, so a moved or replaced
// release can't reach a build. The checksum is the one maplibre-gl-native-distribution publishes for 6.31.0
// (released 2026-09-11). To update: a release at least 3 days old, `swift package compute-checksum` on its zip,
// and both lines below.
import PackageDescription

let package = Package(
    name: "MapLibreBinary",
    platforms: [.iOS(.v12)],
    products: [
        .library(name: "MapLibre", targets: ["MapLibre"]),
    ],
    targets: [
        .binaryTarget(
            name: "MapLibre",
            url: "https://github.com/maplibre/maplibre-native/releases/download/ios-v6.31.0/MapLibre.dynamic.xcframework.zip",
            checksum: "de3aaa435dd86768b06d90245e630d068dd7eef1491afae7217d1654c52c462a"
        ),
    ]
)
