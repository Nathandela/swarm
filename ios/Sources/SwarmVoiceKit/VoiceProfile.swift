import CryptoKit
import Foundation

public enum VoiceProfileError: Error, Equatable {
    case invalidPlace
    case invalidSignatureInput
    case invalidGrant
    case profileConflict
    case corruptProfile
    case keychain(Int32)
}

public struct VoicePlace: Codable, Equatable, Sendable {
    public let id: String
    public let sshAddress: String
    public let sshUser: String
    public let hostKeySHA256: String
    public let loopbackEndpoint: String

    public init(id: String, sshAddress: String, sshUser: String, hostKeySHA256: String, loopbackEndpoint: String) throws {
        self.id = id
        self.sshAddress = sshAddress
        self.sshUser = sshUser
        self.hostKeySHA256 = hostKeySHA256
        self.loopbackEndpoint = loopbackEndpoint
        guard isValid else { throw VoiceProfileError.invalidPlace }
    }

    fileprivate var isValid: Bool {
        let fingerprint = hostKeySHA256.dropFirst("SHA256:".count)
        guard hostKeySHA256.hasPrefix("SHA256:"), fingerprint.utf8.count == 43,
              let bytes = Data(base64Encoded: String(fingerprint) + "="), bytes.count == 32,
              bytes.base64EncodedString() == String(fingerprint) + "=" else { return false }
        guard voiceField(id, limit: 128), transportField(sshAddress, limit: 512),
              transportField(sshUser, limit: 256), let split = sshAddress.lastIndex(of: ":"),
              split != sshAddress.startIndex, validPort(String(sshAddress[sshAddress.index(after: split)...])) else { return false }
        return ["127.0.0.1:", "[::1]:"].contains { prefix in
            loopbackEndpoint.hasPrefix(prefix) && validPort(String(loopbackEndpoint.dropFirst(prefix.count)))
        }
    }
}

public enum VoiceKeyPurpose: Sendable {
    case ssh, enrollment
}

public struct VoiceProfile: Sendable {
    fileprivate let record: ProfileRecord

    public var place: VoicePlace { record.place }
    public var grantJSON: String? { record.grantJSON }

    public func publicKey(for purpose: VoiceKeyPurpose) throws -> String {
        try privateKey(purpose).publicKey.rawRepresentation.base64EncodedString()
    }

    public func sign(_ messageBase64: String, for purpose: VoiceKeyPurpose) throws -> String {
        let limit = 128 << 10
        guard messageBase64.utf8.count <= 4 * ((limit + 2) / 3),
              let message = Data(base64Encoded: messageBase64), !message.isEmpty, message.count <= limit,
              message.base64EncodedString() == messageBase64 else { throw VoiceProfileError.invalidSignatureInput }
        return try privateKey(purpose).signature(for: message).base64EncodedString()
    }

    private func privateKey(_ purpose: VoiceKeyPurpose) throws -> Curve25519.Signing.PrivateKey {
        try Curve25519.Signing.PrivateKey(rawRepresentation: purpose == .ssh ? record.sshSeed : record.enrollmentSeed)
    }
}

fileprivate struct ProfileRecord: Codable, Equatable, Sendable {
    let version: Int
    let place: VoicePlace
    let sshSeed: Data
    let enrollmentSeed: Data
    var grantJSON: String?
}

package protocol VoiceProfileBackend: Sendable {
    func read(_ id: String) throws -> Data?
    func add(_ data: Data, id: String) throws -> Bool
    func update(_ data: Data, id: String) throws
}

// Keeps the place, both independent signing keys and the exact Go grant in one Keychain item.
public actor VoiceProfileStore {
    private let backend: any VoiceProfileBackend

    public init(keychainService: String) throws {
        guard voiceField(keychainService, limit: 256) else { throw VoiceProfileError.invalidPlace }
        self.backend = KeychainVoiceProfileBackend(service: keychainService)
    }

    package init(backend: any VoiceProfileBackend) {
        self.backend = backend
    }

    public func load(placeID: String) throws -> VoiceProfile? {
        guard voiceField(placeID, limit: 128) else { throw VoiceProfileError.invalidPlace }
        guard let raw = try backend.read(placeID) else { return nil }
        guard raw.count <= 8192, let record = try? JSONDecoder().decode(ProfileRecord.self, from: raw),
              record.version == 1, record.place.id == placeID, record.place.isValid,
              record.sshSeed.count == 32, record.enrollmentSeed.count == 32, record.sshSeed != record.enrollmentSeed,
              record.grantJSON.map({ validGrant($0, placeID: placeID) }) ?? true else {
            throw VoiceProfileError.corruptProfile
        }
        return VoiceProfile(record: record)
    }

    public func provision(_ place: VoicePlace) throws -> VoiceProfile {
        guard place.isValid else { throw VoiceProfileError.invalidPlace }
        if let existing = try load(placeID: place.id) {
            guard existing.place == place else { throw VoiceProfileError.profileConflict }
            return existing
        }
        let record = ProfileRecord(version: 1, place: place,
            sshSeed: Curve25519.Signing.PrivateKey().rawRepresentation,
            enrollmentSeed: Curve25519.Signing.PrivateKey().rawRepresentation)
        if try backend.add(JSONEncoder().encode(record), id: place.id) {
            return VoiceProfile(record: record)
        }
        guard let existing = try load(placeID: place.id), existing.place == place else {
            throw VoiceProfileError.profileConflict
        }
        return existing
    }

    // Retains the Go envelope byte-for-byte. Go verifies its profile digest before using it.
    public func storeEnrollment(_ raw: String, for profile: VoiceProfile) throws {
        guard validGrant(raw, placeID: profile.place.id) else { throw VoiceProfileError.invalidGrant }
        guard var current = try load(placeID: profile.place.id)?.record,
              current.place == profile.place, current.sshSeed == profile.record.sshSeed,
              current.enrollmentSeed == profile.record.enrollmentSeed else { throw VoiceProfileError.profileConflict }
        if let prior = current.grantJSON {
            guard prior == raw else { throw VoiceProfileError.profileConflict }
            return
        }
        current.grantJSON = raw
        try backend.update(JSONEncoder().encode(current), id: current.place.id)
    }
}

private struct EnrollmentEnvelope: Decodable {
    struct Credential: Decodable {
        let place_id: String
        let device_id: String
        let generation: UInt64
        let token: String
    }
    let profile: String
    let credential: Credential
}

private func validGrant(_ raw: String, placeID: String) -> Bool {
    guard raw.utf8.count <= 2048,
          let grant = try? JSONDecoder().decode(EnrollmentEnvelope.self, from: Data(raw.utf8)) else { return false }
    return grant.credential.place_id == placeID && grant.credential.generation > 0 &&
        lowercaseHex(grant.profile, bytes: 32) && lowercaseHex(grant.credential.device_id, bytes: 16) &&
        lowercaseHex(grant.credential.token, bytes: 32)
}

private func lowercaseHex(_ value: String, bytes: Int) -> Bool {
    value.utf8.count == bytes * 2 && value.utf8.allSatisfy { (48...57).contains($0) || (97...102).contains($0) }
}

private func voiceField(_ value: String, limit: Int) -> Bool {
    !value.isEmpty && value.utf8.count <= limit && value == value.trimmingCharacters(in: .whitespacesAndNewlines) &&
        !value.unicodeScalars.contains(where: CharacterSet.controlCharacters.contains)
}


private func transportField(_ value: String, limit: Int) -> Bool {
    voiceField(value, limit: limit) && !value.unicodeScalars.contains(where: CharacterSet.whitespacesAndNewlines.contains)
}

private func validPort(_ raw: String) -> Bool {
    guard let port = UInt16(raw), port > 0 else { return false }
    return String(port) == raw
}
