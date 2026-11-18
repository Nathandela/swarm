// swift-tools-version: 5.9
import PackageDescription

let package = Package(
    name: "SwarmVoiceKit",
    platforms: [.macOS(.v10_15), .iOS(.v13)],
    products: [.library(name: "SwarmVoiceKit", targets: ["SwarmVoiceKit"])],
    targets: [
        .target(name: "SwarmVoiceKit"),
        .executableTarget(name: "VoiceKitChecks", dependencies: ["SwarmVoiceKit"])
    ]
)
