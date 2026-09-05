package phonecore

import (
	"encoding/base64"
	"errors"
	"time"
)

// PhoneBinding is the relay-v2 authority for one phone mailbox generation.
// Active distinguishes an ordinary reconnect from a generation retired by pairing.
type PhoneBinding struct {
	Home       string `json:"home"`
	PhoneRID   string `json:"phone_rid"`
	Generation uint64 `json:"generation,string"`
	Active     bool   `json:"active"`
}

var (
	ErrPhoneBindingStale   = errors.New("phonecore: phone binding generation is stale")
	ErrPhoneBindingChanged = errors.New("phonecore: phone binding changed")
)

func validPhoneBinding(b PhoneBinding) bool {
	return b.Active && b.Generation != 0 && validLowerHex(b.Home, 64) && validLowerHex(b.PhoneRID, 32)
}

func validLowerHex(value string, size int) bool {
	if len(value) != size {
		return false
	}
	for i := range value {
		if c := value[i]; c < '0' || (c > '9' && c < 'a') || c > 'f' {
			return false
		}
	}
	return true
}

func validPhoneIncarnation(incarnation string) bool {
	if len(incarnation) != 22 {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(incarnation)
	return err == nil && len(raw) == 16 && base64.RawURLEncoding.EncodeToString(raw) == incarnation
}

func validRecoveryToken(token string) bool { return validLowerHex(token, 32) }

// PhoneBinding returns the persisted binding, including an inactive generation floor.
func (c *Core) PhoneBinding() (PhoneBinding, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.st.phoneBinding, c.st.phoneBinding.Generation != 0
}

// ActivatePhoneBinding persists the relay-authenticated authority before Subscribe. An
// exact active binding is an ordinary reconnect. Pairing retires that binding; the same
// home and phone RID must then present a strictly newer generation.
func (c *Core) ActivatePhoneBinding(next PhoneBinding) error {
	if !validPhoneBinding(next) {
		return errors.New("phonecore: invalid active phone binding")
	}
	c.router.acceptMu.Lock()
	defer c.router.acceptMu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()

	current := c.st.phoneBinding
	sameScope := current.Home == next.Home && current.PhoneRID == next.PhoneRID
	if current.Active {
		if current == next {
			return nil
		}
		return ErrPhoneBindingChanged
	}
	if sameScope && current.Generation != 0 && next.Generation <= current.Generation {
		return ErrPhoneBindingStale
	}
	st := c.st.clone()
	st.phoneBinding = next
	if !sameScope || current.Generation != next.Generation {
		st.RelayCursor, st.RelayIncarnation = 0, ""
	}
	st = c.stateForPersistLocked(st)
	st.phoneBinding = next
	err := c.store.ActivatePhoneBinding(st.clone())
	if err != nil && !atomicWriteCommitted(err) {
		return err
	}
	c.st = loadCoreState(c.store)
	return err
}

// SetPhoneIncarnation binds the native relay-v2 checkpoint to the exact live authority.
func (c *Core) SetPhoneIncarnation(binding PhoneBinding, incarnation string) error {
	if !validPhoneIncarnation(incarnation) {
		return errors.New("phonecore: invalid relay-v2 mailbox incarnation")
	}
	c.router.acceptMu.Lock()
	defer c.router.acceptMu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.st.phoneBinding != binding || !binding.Active {
		return ErrPhoneBindingChanged
	}
	if c.st.RelayIncarnation == incarnation {
		return nil
	}
	st := c.st.clone()
	st.RelayIncarnation = incarnation
	return c.persistLocked(st)
}

// CommitPhonePairing makes the new pin, optional staged push ownership, old-checkpoint
// reset, and retirement of the old relay generation one durable transaction. The caller
// stops and joins the old transport before entering this method. fn runs with Core.mu held
// and may mutate only the State it receives.
func (c *Core) CommitPhonePairing(staged *PushAddress, fn func(*State)) error {
	c.router.acceptMu.Lock()
	c.mu.Lock()

	st := c.st.clone()
	if staged != nil {
		enc := EncodePushAddress(*staged)
		if st.pairingPushOwned != "" && st.pairingPushOwned != enc {
			c.mu.Unlock()
			c.router.acceptMu.Unlock()
			return errors.New("phonecore: another staged push binding ownership decision is pending")
		}
		if !containsString(c.push.data.PendingPairingRevokes, enc) || c.push.binding(enc) == nil {
			c.mu.Unlock()
			c.router.acceptMu.Unlock()
			return errors.New("phonecore: cannot own a push binding that is not durably staged")
		}
		st.pairingPushOwned = enc
	}
	if fn != nil {
		fn(&st)
	}
	retired := st.phoneBinding
	if retired.Generation != 0 {
		retired.Active = false
	}
	st.RelayCursor, st.RelayIncarnation = 0, ""
	st = c.stateForPersistLocked(st)
	st.phoneBinding = retired
	err := c.store.CommitPhonePairing(st.clone())
	if err != nil && !atomicWriteCommitted(err) {
		c.mu.Unlock()
		c.router.acceptMu.Unlock()
		return err
	}
	c.st = loadCoreState(c.store)
	c.mu.Unlock()
	c.rebind()
	c.router.acceptMu.Unlock()
	return err
}

// AcceptPhoneDelivery is the only relay-v2 receive entry point. acceptMu prevents a pairing
// retirement from crossing the receive transaction; the binding is rechecked with Core.mu
// held immediately before any parsing, durable progress, or ACK can occur.
func (c *Core) AcceptPhoneDelivery(binding PhoneBinding, raw []byte, cursor uint64) (Receipt, error) {
	c.router.acceptMu.Lock()
	defer c.router.acceptMu.Unlock()
	c.mu.Lock()
	current := c.st.phoneBinding
	c.mu.Unlock()
	if current != binding || !binding.Active {
		return Receipt{}, ErrPhoneBindingChanged
	}
	return c.router.acceptCommitAtLocked(raw, cursor, time.Now())
}

func validatePhoneBindingState(binding PhoneBinding) error {
	if binding == (PhoneBinding{}) {
		return nil
	}
	binding.Active = true
	if !validPhoneBinding(binding) {
		return errors.New("malformed phone binding")
	}
	return nil
}
