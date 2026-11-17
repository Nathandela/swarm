import Foundation
import SwarmVoiceKit
import Swarmmobile

public enum VoiceDeviceBridgeError: Error {
    case rejectedProfile
}

public enum VoiceDeviceBridge {
    // Constructing the handle validates the pinned profile. Its synchronous
    // network methods belong on a caller-owned background task.
    public static func makeDevice(for profile: VoiceProfile) throws -> SwarmmobileVoiceDevice {
        let ssh = try SSHSigner(profile: profile)
        let enrollment = try EnrollmentSigner(profile: profile)
        let place = profile.place
        var error: NSError?
        let device = SwarmmobileNewVoiceDevice(place.sshAddress, place.sshUser,
            place.hostKeySHA256, place.loopbackEndpoint, ssh, enrollment, &error)
        if let error { throw error }
        guard let device else {
            throw VoiceDeviceBridgeError.rejectedProfile
        }
        return device
    }

    // The one-use invitation and the grant's profile digest are validated by Go.
    public static func enroll(profile: VoiceProfile, invitationReference: String,
                              store: VoiceProfileStore) async throws -> VoiceProfile {
        guard let current = try await store.load(placeID: profile.place.id),
              current.place == profile.place,
              try current.publicKey(for: .ssh) == profile.publicKey(for: .ssh),
              try current.publicKey(for: .enrollment) == profile.publicKey(for: .enrollment) else {
            throw VoiceProfileError.profileConflict
        }
        if current.grantJSON != nil { return current }
        let device = try makeDevice(for: current)
        defer { try? device.end() }
        var error: NSError?
        let grant = device.enroll(current.place.id, invitationRef: invitationReference,
                                  invitationEndpoint: current.place.loopbackEndpoint, error: &error)
        if let error { throw error }
        try await store.storeEnrollment(grant, for: profile)
        guard let saved = try await store.load(placeID: profile.place.id) else {
            throw VoiceDeviceBridgeError.rejectedProfile
        }
        return saved
    }
}

private final class SSHSigner: NSObject, SwarmmobileVoiceSSHSignerProtocol {
    private let profile: VoiceProfile
    private let key: String

    init(profile: VoiceProfile) throws {
        self.profile = profile
        self.key = try profile.publicKey(for: .ssh)
    }

    func voiceSSHPublicKey() -> String { key }

    func signVoiceSSH(_ messageBase64: String?, error: NSErrorPointer) -> String {
        sign(messageBase64, for: .ssh, profile: profile, error: error)
    }
}

private final class EnrollmentSigner: NSObject, SwarmmobileVoiceEnrollmentSignerProtocol {
    private let profile: VoiceProfile
    private let key: String

    init(profile: VoiceProfile) throws {
        self.profile = profile
        self.key = try profile.publicKey(for: .enrollment)
    }

    func voiceEnrollmentPublicKey() -> String { key }

    func signVoiceEnrollment(_ messageBase64: String?, error: NSErrorPointer) -> String {
        sign(messageBase64, for: .enrollment, profile: profile, error: error)
    }
}


private func sign(_ messageBase64: String?, for purpose: VoiceKeyPurpose, profile: VoiceProfile, error output: NSErrorPointer) -> String {
    do {
        guard let messageBase64 else { throw VoiceProfileError.invalidSignatureInput }
        return try profile.sign(messageBase64, for: purpose)
    } catch {
        output?.pointee = error as NSError
        return ""
    }
}
