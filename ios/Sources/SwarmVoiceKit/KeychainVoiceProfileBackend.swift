import Foundation
import Security

struct KeychainVoiceProfileBackend: VoiceProfileBackend {
    let service: String

    private func query(_ id: String) -> [String: Any] {
        var fields: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: id,
            kSecAttrSynchronizable as String: false
        ]
        #if os(macOS)
        fields[kSecUseDataProtectionKeychain as String] = true
        #endif
        return fields
    }

    func read(_ id: String) throws -> Data? {
        var fields = query(id)
        fields[kSecReturnData as String] = true
        fields[kSecMatchLimit as String] = kSecMatchLimitOne
        var result: CFTypeRef?
        let status = SecItemCopyMatching(fields as CFDictionary, &result)
        if status == errSecItemNotFound { return nil }
        guard status == errSecSuccess else { throw VoiceProfileError.keychain(status) }
        guard let data = result as? Data else { throw VoiceProfileError.corruptProfile }
        return data
    }

    func add(_ data: Data, id: String) throws -> Bool {
        var fields = query(id)
        fields[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        fields[kSecValueData as String] = data
        let status = SecItemAdd(fields as CFDictionary, nil)
        if status == errSecDuplicateItem { return false }
        guard status == errSecSuccess else { throw VoiceProfileError.keychain(status) }
        return true
    }

    func update(_ data: Data, id: String) throws {
        let fields: [String: Any] = [kSecValueData as String: data]
        let status = SecItemUpdate(query(id) as CFDictionary, fields as CFDictionary)
        guard status == errSecSuccess else { throw VoiceProfileError.keychain(status) }
    }
}
