package skeleton

// APPLY BY INJECTION -- mirror-program.md section 3, the M1 design decision.
//
// The phone's Allow/Deny is applied by writing the CLI's OWN dialog keys into the PTY the
// daemon already owns, gated on the live grid still showing that dialog. This file will hold
// the daemon-side primitive; approval.go holds the validation that decides whether to call it.

import "time"

// injectWatchdogDelay is how long the daemon waits before looking again at a dialog it has
// just typed at. It is not a timeout on anything: the injection is complete the moment the
// bytes are written, and this only decides how soon SILENCE becomes a note on the transcript.
//
// A var rather than a const so a test can shorten it; production never writes it.
var injectWatchdogDelay = 5 * time.Second
