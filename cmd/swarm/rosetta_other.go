//go:build !darwin

package main

// rosettaTranslated is darwin-only: Rosetta 2 is an Apple-Silicon translation
// layer, so on every other platform this process is never translated (bead 8c0).
func rosettaTranslated() bool { return false }
