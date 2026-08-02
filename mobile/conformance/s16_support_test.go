package conformance_test

// Slice S16 (the phone app) -- shared scaffolding for the RUNTIME half.
//
// WHY EVERY S16 VERB IS RESOLVED BY NAME. This directory is ONE Go package, so a direct
// call to a facade verb that does not exist yet fails the whole DIRECTORY to compile: one
// lump error covering sixteen requirements, and every already-shipped slice's conformance
// test taken down with them. Resolved by name, a missing verb fails exactly the test that
// needs it, with the signature it must have written into the failure message -- which is
// the RED this repo asks for ("one message per requirement", facadesource_test.go).
//
// The tradeoff is stated rather than hidden: reflection cannot catch a signature mistake at
// compile time, so every call site below pins the method's full type string and fails on a
// mismatch. A verb that exists with the wrong shape is as loud as one that is missing.

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// s16Missing is the shared prose for a verb S16 requires and the facade does not export.
// It names the requirement, the exact signature, and why the verb cannot be dropped -- an
// implementer reading only this line has enough.
func s16Missing(recv any, name, sig, req, why string) string {
	return fmt.Sprintf("%s: %T has no method %s.\n\tREQUIRED SIGNATURE: %s %s\n\t%s\n\t"+
		"Adding it moves mobile/testdata/exported_surface.golden (a REVIEWED change, "+
		"PB-BIND-7) and needs a screen_coverage.tsv row (PB-BIND-3).",
		req, recv, name, name, sig, why)
}

// s16Verb invokes an S16-required facade verb by name, pinning its signature.
func s16Verb(t *testing.T, recv any, name, sig, req, why string, args ...any) []reflect.Value {
	t.Helper()
	m := s16Lookup(t, recv, name, sig, req, why)
	in := make([]reflect.Value, 0, len(args))
	for _, a := range args {
		in = append(in, reflect.ValueOf(a))
	}
	return m.Call(in)
}

// s16Lookup resolves the verb without calling it, for a test that only needs to know
// whether it exists (or that needs to call it many times).
func s16Lookup(t *testing.T, recv any, name, sig, req, why string) reflect.Value {
	t.Helper()
	m := reflect.ValueOf(recv).MethodByName(name)
	if !m.IsValid() {
		t.Fatalf("%s", s16Missing(recv, name, sig, req, why))
	}
	if got := m.Type().String(); got != "func"+sig {
		t.Fatalf("%s: %T.%s has signature %s, want func%s. %s",
			req, recv, name, got, sig, why)
	}
	return m
}

// s16Has reports whether a verb exists, for the sweep -- which must enumerate the surface
// rather than assert on one member of it.
func s16Has(recv any, name string) bool {
	return reflect.ValueOf(recv).MethodByName(name).IsValid()
}

// ---- result unpacking ----------------------------------------------------------
//
// gomobile's shape is (value, error) or bare error, so these three cover the surface.

func s16Err(t *testing.T, out []reflect.Value) error {
	t.Helper()
	if len(out) == 0 {
		t.Fatalf("verb returned no results; every facade entry point returns at least an error (PB-BIND-5)")
	}
	return s16AsError(t, out[len(out)-1])
}

func s16StringErr(t *testing.T, out []reflect.Value) (string, error) {
	t.Helper()
	if len(out) != 2 {
		t.Fatalf("verb returned %d results, want (string, error)", len(out))
	}
	return out[0].String(), s16AsError(t, out[1])
}

func s16IntErr(t *testing.T, out []reflect.Value) (int, error) {
	t.Helper()
	if len(out) != 2 {
		t.Fatalf("verb returned %d results, want (int, error)", len(out))
	}
	return int(out[0].Int()), s16AsError(t, out[1])
}

func s16AsError(t *testing.T, v reflect.Value) error {
	t.Helper()
	if v.IsNil() {
		return nil
	}
	err, ok := v.Interface().(error)
	if !ok {
		t.Fatalf("last result is %s, not error", v.Type())
	}
	return err
}

// ---- the pinned surface and the taxonomy, read from disk ------------------------
//
// PB-APP-9's closed set is the GOLDEN, so the runtime sweep enumerates from the same
// artifact the source-level fence in ../s16_taxonomy_test.go does. Two tests reading one
// file is the point: a verb added to the facade reaches both, and a verb added to only one
// of the two lists reaches neither.

func s16RepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", dir)
		}
		dir = parent
	}
}

// s16GoldenMethods returns every `method App.<Name>(...)` on the pinned surface.
func s16GoldenMethods(t *testing.T) []string {
	t.Helper()
	path := filepath.Join(s16RepoRoot(t), "mobile", "testdata", "exported_surface.golden")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("PB-APP-9's sweep enumerates from the pinned surface: %v", err)
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "method App.")
		if !ok {
			continue
		}
		name, _, ok := strings.Cut(rest, "(")
		if ok && name != "" {
			out = append(out, name)
		}
	}
	if len(out) == 0 {
		t.Fatalf("the pinned surface lists no App methods; the sweep would be vacuous")
	}
	return out
}

// s16TaxonomyTokens returns the set of error-class TOKENS the checked-in taxonomy declares,
// and the one reserved for "this facade did not classify it".
func s16TaxonomyTokens(t *testing.T) (tokens map[string]string, unknown string) {
	t.Helper()
	path := filepath.Join(s16RepoRoot(t), "mobile", "error_taxonomy.tsv")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("PB-APP-9 requires a checked-in error taxonomy at %s: %v\n"+
			"See mobile/s16_taxonomy_test.go for the column contract; this sweep reads the "+
			"class_const and token columns and the reserved ErrClassUnknown row.", path, err)
	}
	tokens = map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) < 2 {
			continue
		}
		class, token := strings.TrimSpace(cols[0]), strings.TrimSpace(cols[1])
		tokens[token] = class
		if class == "ErrClassUnknown" {
			unknown = token
		}
	}
	if unknown == "" {
		t.Fatalf("PB-APP-9: the taxonomy declares no ErrClassUnknown row.\n" +
			"The classifier MUST be able to say \"I do not know this error\", or the sweep below " +
			"is vacuous: a classifier that answered ErrClassInternal for every input would pass " +
			"every other check in this file. ErrClassUnknown is what makes the sweep's assertion " +
			"-- that NO facade error lands there -- a real one.")
	}
	return tokens, unknown
}

// s16BoolVerb resolves and calls a `() (bool, error)` accessor -- the shape every read-model
// staleness flag has to take, since gomobile binds a struct field and a method identically
// from Kotlin's side but only a method can be added to an existing handle without moving
// every field of it.
func s16BoolVerb(t *testing.T, recv any, name, req, why string) (bool, error) {
	t.Helper()
	m := s16Lookup(t, recv, name, "() (bool, error)", req, why)
	out := m.Call(nil)
	if len(out) != 2 {
		t.Fatalf("%s: %T.%s returned %d results, want (bool, error)", req, recv, name, len(out))
	}
	return out[0].Bool(), s16AsError(t, out[1])
}

// s16Args builds the reflect.Value argument list for a resolved verb.
func s16Args(args ...any) []reflect.Value {
	in := make([]reflect.Value, 0, len(args))
	for _, a := range args {
		in = append(in, reflect.ValueOf(a))
	}
	return in
}

func s16BoolErr(t *testing.T, out []reflect.Value) (bool, error) {
	t.Helper()
	if len(out) != 2 {
		t.Fatalf("verb returned %d results, want (bool, error)", len(out))
	}
	return out[0].Bool(), s16AsError(t, out[1])
}

// s16IntVerb resolves and calls an `() (int, error)` accessor.
func s16IntVerb(t *testing.T, recv any, name, req, why string) (int, error) {
	t.Helper()
	m := s16Lookup(t, recv, name, "() (int, error)", req, why)
	return s16IntErr(t, m.Call(nil))
}
