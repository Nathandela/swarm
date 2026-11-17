# Android voice manager

Open **Settings → Voice manager**. On the host, configure a stable, nonzero device listener port (for example `--device-listen 127.0.0.1:32100`). Keep that exact loopback address and port assigned to the same place. The app does not rediscover ephemeral endpoints or reuse a stored grant with a different profile. This is a separate device route using app-owned SSH and a scoped host grant. It uses neither the dashboard pairing nor the Android push identity.

1. Generate the two app keys. Install the displayed OpenSSH public key on the selected computer by a trusted administrative route. Run `swarm voice enroll --device-key <enrollment-public-key>` on that computer with the separately displayed enrollment key.
2. Enter the SSH address, user and verified OpenSSH SHA256 host-key fingerprint. Scan or paste the invitation JSON, or enter its place, reference and remote loopback address manually. The app requires the exact invitation endpoint for imported invitations.
3. Enroll, then use **Connect saved grant** after reopening the screen. The client can send text and look up a request's receipt and canonical manager publications. Keep the request ID when delivery is uncertain. The app does not automatically resubmit requests.

The two Ed25519 private keys stay in independent Android Keystore entries. The app stores the public identity bindings, SSH profile and scoped grant in an app-private, no-backup file encrypted with a separate Android Keystore AES-GCM key. App signers run off the UI thread. Credentials and signed buffers must not be logged. A host-side `swarm voice revoke --id <device-id>` is separate from **End voice connection**.

Recording starts only from the visible voice screen after microphone and notification permission. The non-exported microphone service retains the exact request across Activity recreation. Its ongoing notification offers **Stop and send** and **Discard**. The app stops without sending on unsupported PCM format, permission or focus loss, route changes, capture limit or discard. End closes the owned connection. If finalization already started, check the receipt. There is no service restart or automatic audio replay.

Requires the app's supported Android 13+ floor, Android Keystore Ed25519 support, a microphone route that accepts 24 kHz mono PCM, and SSH direct forwarding to the enrolled loopback endpoint. Platform key providers, Bluetooth routing, lock/app-switch behavior and real microphone capture still need physical-device qualification. This screen does not play audio.
