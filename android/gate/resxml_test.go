package gate

import (
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// THE CLASS THIS CATCHES IS THE ONE EVERY OTHER GATE IN THIS PACKAGE IS BLIND TO.
//
// Every check over `res/` in this package reads those files as TEXT -- it greps for a token name, a
// stroke width, a colour reference. None of them parses the XML, so a resource that is not
// well-formed passes all of them and then fails at `aapt` with a message about a row and a column.
// It has happened twice. Both times the cause was the same: a comment citing a CSS custom property
// by its real name (`--p-ink3`) or using this repo's `--` em-dash convention, and an XML comment
// may not contain a double hyphen. It is a parse error in the specification, not a lint, and no
// amount of Kotlin correctness prevents it.
//
// So this decodes rather than greps. `encoding/xml` rejects the double hyphen for the same reason
// aapt does -- "invalid sequence \"--\" not allowed in comments" -- which means this check is the
// whole class and not the one instance: an unclosed tag, a bad entity or a stray `<` fails here
// too, at `go test` speed, without a JDK or an Android SDK on the machine.
//
// It is deliberately NOT joined to a PB-* requirement. Nothing in the specification asks for
// well-formed XML; it is a precondition of the resources existing at all, which is why it went
// unchecked twice. Recorded in ADR-007 B138.
func TestResourceXMLIsWellFormed(t *testing.T) {
	root := filepath.Join(repoRoot(t), filepath.FromSlash("android/app/src/main/res"))

	checked := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".xml") {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Errorf("resource XML: cannot read %s: %v", mustRel(t, path), readErr)
			return nil
		}
		checked++
		if parseErr := xmlWellFormed(raw); parseErr != nil {
			t.Errorf("resource XML: %s is not well-formed and will fail resource compilation "+
				"before any test runs: %v\n"+
				"\tIf this is a double hyphen in a comment, it is almost certainly a CSS token name "+
				"or this repo's em-dash convention. Spell tokens [p-name] inside res/ comments.",
				mustRel(t, path), parseErr)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("resource XML: cannot walk %s: %v", mustRel(t, root), err)
	}
	if checked == 0 {
		t.Fatalf("resource XML: no .xml files found under %s, so this test asserted nothing",
			mustRel(t, root))
	}
	t.Logf("resource XML: %d files parsed", checked)
}

// xmlWellFormed reports the first structural fault in doc, or nil.
func xmlWellFormed(doc []byte) error {
	dec := xml.NewDecoder(strings.NewReader(string(doc)))
	for {
		_, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// TestResourceXMLReaderRejectsTheKnownFault is the negative control, fed to the SAME function the
// walk above calls. Without it the walk passes when every file is fine AND when the reader has
// stopped reading, and those are indistinguishable from a green run.
func TestResourceXMLReaderRejectsTheKnownFault(t *testing.T) {
	// The exact shape that broke the build twice: a token name in a comment.
	twiceBroken := []byte(`<?xml version="1.0" encoding="utf-8"?>` +
		`<!-- the glyph is --p-ink3 -->` +
		`<vector/>`)
	if err := xmlWellFormed(twiceBroken); err == nil {
		t.Error("resource XML: the reader accepts a comment containing a double hyphen, which is " +
			"the one fault this file exists for -- it would have passed both times the build broke")
	}

	if err := xmlWellFormed([]byte(`<?xml version="1.0"?><vector><path/>`)); err == nil {
		t.Error("resource XML: the reader accepts an unclosed element, so it is not parsing")
	}

	// And it must accept the real thing, or every resource in the tree is reported as broken.
	fine := []byte(`<?xml version="1.0" encoding="utf-8"?>` +
		`<!-- the glyph is [p-ink3], spelled so this comment parses -->` +
		`<vector xmlns:android="http://schemas.android.com/apk/res/android"` +
		` android:width="24dp"><path android:pathData="M15 5l-7 7 7 7" /></vector>`)
	if err := xmlWellFormed(fine); err != nil {
		t.Errorf("resource XML: the reader rejects a well-formed vector drawable: %v", err)
	}
}
