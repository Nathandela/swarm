// Package procstart is the process-creation-time identity token shared by the daemon
// (which matches a shim by (pid, start-time), S3/D-4) and the shim (which records the
// same pair for its backend so a RESTARTED daemon can tell a live app-server from an
// unrelated process that inherited a recycled pid, ADR-013 §R7.2c).
//
// IT EXISTS AS ITS OWN PACKAGE FOR ONE REASON: the two readers must produce the SAME
// value or the comparison is meaningless, and internal/shim cannot import
// internal/daemon (the dependency runs the other way, daemon -> shim). A second copy of
// the platform code would be a fact two files could disagree about.
package procstart
