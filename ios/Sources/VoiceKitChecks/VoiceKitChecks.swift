import CryptoKit
import Foundation
import Security
import SwarmVoiceKit

private final class MemoryProfiles: VoiceProfileBackend, @unchecked Sendable {
    private var items: [String: Data] = [:]
    func read(_ id: String) throws -> Data? { items[id] }
    func add(_ data: Data, id: String) throws -> Bool {
        if items[id] != nil { return false }
        items[id] = data
        return true
    }
    func update(_ data: Data, id: String) throws { items[id] = data }
}

private struct LockedProfiles: VoiceProfileBackend {
    func read(_ id: String) throws -> Data? { throw VoiceProfileError.keychain(errSecInteractionNotAllowed) }
    func add(_ data: Data, id: String) throws -> Bool { throw CheckFailure("an inaccessible profile was regenerated") }
    func update(_ data: Data, id: String) throws { throw CheckFailure("an inaccessible profile was changed") }
}

@main
struct VoiceKitChecks {
    static func main() async throws {
        let checks = VoiceKitChecks()
        try await checks.testIndependentEd25519SignersUseCanonicalBase64()
        try await checks.testProfileRetainsExactGrantAndRefusesRetargeting()
        try await checks.testInaccessibleProfileDoesNotRegenerateKeys()
        print("Swift voice profile and signer checks passed")
    }

    private func place(_ id: String = "lab", address: String = "fixture:22") throws -> VoicePlace {
        let pin = String(Data(repeating: 1, count: 32).base64EncodedString().dropLast())
        return try VoicePlace(id: id, sshAddress: address, sshUser: "operator",
                              hostKeySHA256: "SHA256:" + pin, loopbackEndpoint: "127.0.0.1:44001")
    }

    func testInaccessibleProfileDoesNotRegenerateKeys() async throws {
        let store = VoiceProfileStore(backend: LockedProfiles())
        do {
            _ = try await store.provision(place())
            throw CheckFailure("locked profile custody was bypassed")
        } catch VoiceProfileError.keychain(errSecInteractionNotAllowed) {}
    }

    func testIndependentEd25519SignersUseCanonicalBase64() async throws {
        let store = VoiceProfileStore(backend: MemoryProfiles())
        let profile = try await store.provision(place())
        let raw = Data("fixed bytes".utf8)
        let encoded = raw.base64EncodedString()
        let ssh = try unwrap(Data(base64Encoded: profile.publicKey(for: .ssh)))
        let enrollment = try unwrap(Data(base64Encoded: profile.publicKey(for: .enrollment)))
        try check(ssh != enrollment)
        let signed = try unwrap(Data(base64Encoded: profile.sign(encoded, for: .ssh)))
        try check(Curve25519.Signing.PublicKey(rawRepresentation: ssh).isValidSignature(signed, for: raw))
        try check(!Curve25519.Signing.PublicKey(rawRepresentation: enrollment).isValidSignature(signed, for: raw))
        try reject { _ = try profile.sign(encoded + "\n", for: .ssh) }
        try reject {
            _ = try profile.sign(Data(repeating: 0, count: (128 << 10) + 1).base64EncodedString(), for: .ssh)
        }
    }

    func testProfileRetainsExactGrantAndRefusesRetargeting() async throws {
        let backend = MemoryProfiles()
        let original = VoiceProfileStore(backend: backend)
        let profile = try await original.provision(place())
        let grant = """
        {"profile":"\(String(repeating: "a", count: 64))","credential":{"place_id":"lab","device_id":"\(String(repeating: "b", count: 32))","generation":1,"token":"\(String(repeating: "c", count: 64))"}}
        """
        try await original.storeEnrollment(grant, for: profile)
        let reopened = VoiceProfileStore(backend: backend)
        let loaded = try await reopened.load(placeID: "lab")
        let saved = try unwrap(loaded)
        try check(saved.grantJSON == grant)
        try check(saved.publicKey(for: .ssh) == profile.publicKey(for: .ssh))
        do {
            _ = try await reopened.provision(place(address: "other:22"))
            throw CheckFailure("changed host reused the original profile")
        } catch VoiceProfileError.profileConflict {}
        do {
            try await reopened.storeEnrollment(grant.replacingOccurrences(of: "lab", with: "other"), for: saved)
            throw CheckFailure("wrong-place grant was stored")
        } catch VoiceProfileError.invalidGrant {}
    }
}


private struct CheckFailure: Error {
    let details: String
    init(_ details: String) { self.details = details }
}

private func check(_ condition: @autoclosure () throws -> Bool, line: UInt = #line) throws {
    guard try condition() else { throw CheckFailure("check failed on line \(line)") }
}

private func unwrap<T>(_ value: T?, line: UInt = #line) throws -> T {
    guard let value else { throw CheckFailure("unexpected nil on line \(line)") }
    return value
}

private func reject(_ operation: () throws -> Void) throws {
    do {
        try operation()
        throw CheckFailure("invalid signature input was accepted")
    } catch VoiceProfileError.invalidSignatureInput {}
}
