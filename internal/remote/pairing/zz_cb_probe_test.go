package pairing

// TEMPORARY DEBUGGING PROBE -- delete once measured.
import (
	"testing"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

func TestTMP_ChannelBindingAvailabilityOnTheDevice(t *testing.T) {
	mID, _ := crypto.GenerateIdentity()
	dID, _ := crypto.GenerateIdentity()
	secret, rid := fill32(0x5A), fill16(0x11)

	dev, _ := crypto.NewNoise(crypto.NoiseConfig{Initiator: true, Static: dID.NoiseStatic(), AllowUnpinnedPeer: true, PSK: secret[:], Prologue: crypto.PairPrologue(rid[:])})
	mach, _ := crypto.NewNoise(crypto.NoiseConfig{Initiator: false, Static: mID.NoiseStatic(), AllowUnpinnedPeer: true, PSK: secret[:], Prologue: crypto.PairPrologue(rid[:])})

	msg1, _ := dev.WriteMessage(nil)
	_, _ = mach.ReadMessage(msg1)
	msg2, _ := mach.WriteMessage([]byte("m"))
	_, _ = dev.ReadMessage(msg2)

	t.Logf("AFTER msg2: device binding len=%d complete=%v | machine binding len=%d complete=%v",
		len(dev.ChannelBinding()), dev.HandshakeComplete(), len(mach.ChannelBinding()), mach.HandshakeComplete())

	msg3, _ := dev.WriteMessage([]byte("d"))
	t.Logf("AFTER device WRITES msg3 (not yet sent): device binding len=%d complete=%v", len(dev.ChannelBinding()), dev.HandshakeComplete())
	_, _ = mach.ReadMessage(msg3)
	t.Logf("AFTER machine READS msg3: machine binding len=%d complete=%v; bindings equal=%v",
		len(mach.ChannelBinding()), mach.HandshakeComplete(), string(dev.ChannelBinding()) == string(mach.ChannelBinding()))
}
