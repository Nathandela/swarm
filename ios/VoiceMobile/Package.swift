// swift-tools-version: 5.9
import PackageDescription

let package = Package(
    name: "SwarmVoiceMobile",
    platforms: [.iOS(.v13)],
    products: [.library(name: "SwarmVoiceMobile", targets: ["SwarmVoiceMobile"])],
    dependencies: [.package(name: "SwarmVoiceKit", path: "..")],
    targets: [
        .binaryTarget(name: "Swarmmobile", path: "Artifacts/Swarmmobile.xcframework"),
        .target(name: "SwarmVoiceMobile", dependencies: [
            "Swarmmobile",
            .product(name: "SwarmVoiceKit", package: "SwarmVoiceKit")
        ])
    ]
)
