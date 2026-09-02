package remotegw

import (
	"encoding/hex"
	"path/filepath"
)

// WakeSeqPath returns the durable sequence source for one pairing's WakeV1 stream.
// Address-scoping is load-bearing: a failed test wake burns a reservation block, and a
// replacement allocation must still begin at seq=1 under its fresh address/key rather
// than inheriting that abandoned coordinate.
func WakeSeqPath(remoteDir string, addr PushAddress) string {
	return filepath.Join(remoteDir, "outbound-wake-"+hex.EncodeToString(addr[:])+".seq")
}
