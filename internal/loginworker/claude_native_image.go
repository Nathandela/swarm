package loginworker

import "os"

// OpenClaudeNativeImage retains the already-qualified native executable inode.
// It grants no helper/auth authority; the caller still fences the actual birth.
// Keeping the returned file through exec prevents atomic path replacement from
// redirecting the child. Unsupported platforms retain the helper's refusal.
func OpenClaudeNativeImage(path, version, fingerprint string) (*os.File, error) {
	return openClaudeAuthImage(ClaudeAuthConfig{NativePath: path, NativeVersion: version, NativeFingerprint: fingerprint})
}
