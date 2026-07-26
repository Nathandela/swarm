#!/bin/bash
# TEMPORARY mutation harness -- deleted after the evidence run. The tree is COMMITTED, so
# `git checkout` restores the fix rather than discarding it.
set -u
cd /Users/Nathan/Code/swarm/.claude/worktrees/agent-a9f7f383db492efd4 || exit 1

case "${1:-}" in
nosig)
  python3 - <<'PY'
p = 'internal/remote/relay/server.go'
s = open(p).read()
old = '''	if len(req.ConsentSig) != ed25519.SignatureSize ||
		!ed25519.Verify(ed25519.PublicKey(req.DevicePub), ConsentMessage(sc.rid), req.ConsentSig) {
		return sc.replyErr(codeNotAuthorized)
	}
'''
assert s.count(old) == 1
open(p, 'w').write(s.replace(old, ''))
print('MUTATED: consent verification removed from handleAuthorizeDevice')
PY
  ;;
bearer)
  python3 - <<'PY'
p = 'internal/remote/relay/server.go'
s = open(p).read()
old = 'ConsentMessage(sc.rid), req.ConsentSig)'
new = 'ConsentMessage(RoutingID(ed25519.PublicKey(req.DevicePub))), req.ConsentSig)'
assert s.count(old) == 1
open(p, 'w').write(s.replace(old, new))
print('MUTATED: consent no longer names the GRANTEE (bearer token)')
PY
  ;;
oneedge)
  python3 - <<'PY'
p = 'internal/remote/relay/store.go'
s = open(p).read()
old = '''		if err := pb.Put(pairKey(device, pairer), []byte{1}); err != nil {
			return err
		}
'''
assert s.count(old) == 1
open(p, 'w').write(s.replace(old, ''))
print('MUTATED: authorizePair no longer records the proven device->pairer edge')
PY
  ;;
staleconsent)
  python3 - <<'PY'
p = 'internal/remote/pairing/pairing.go'
s = open(p).read()
old = '	consent, err := p.Consent(machPayload)'
new = '	consent, err := p.Consent(MachinePayload{RelaySPKIPin: machPayload.RelaySPKIPin})'
assert s.count(old) == 1
open(p, 'w').write(s.replace(old, new))
print('MUTATED: consent signed over a guessed machine, not the authenticated msg2')
PY
  ;;
noconsentwire)
  python3 - <<'PY'
p = 'internal/remote/pairing/pairing.go'
s = open(p).read()
old = '	b = appendField(b, p.ConsentSig)\n\treturn b'
new = '	b = appendField(b, nil)\n\treturn b'
assert s.count(old) == 1
open(p, 'w').write(s.replace(old, new))
print('MUTATED: msg3 carries no consent')
PY
  ;;
nopinwire)
  python3 - <<'PY'
p = 'internal/remote/pairing/pairing.go'
s = open(p).read()
old = '	b = appendField(b, p.RelaySPKIPin)\n'
assert s.count(old) == 1
open(p, 'w').write(s.replace(old, '	b = appendField(b, nil)\n'))
print('MUTATED: msg2 carries no relay SPKI pin')
PY
  ;;
enrollopen)
  python3 - <<'PY'
p = 'internal/remote/enroll/enroll.go'
s = open(p).read()
old = '''	if len(out.Device.ConsentSig) == 0 {
		return Result{}, ErrNoConsent
	}
'''
assert s.count(old) == 1
open(p, 'w').write(s.replace(old, ''))
print('MUTATED: enrollment admits a device that granted no route')
PY
  ;;
nopinstate)
  python3 - <<'PY'
p = 'internal/phonecore/state.go'
s = open(p).read()
old = '\t\tRelaySPKIPin:        f.RelaySPKIPin,\n'
assert s.count(old) == 1
open(p, 'w').write(s.replace(old, ''))
print('MUTATED: the phone drops the relay pin on load')
PY
  ;;
*) echo "unknown mutation"; exit 1 ;;
esac
exit 0
