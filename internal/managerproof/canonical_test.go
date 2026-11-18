package managerproof

import (
	"strings"
	"testing"
)

func TestCanonicalArgumentsPreserveNumbersSortKeysAndRejectRepair(t *testing.T) {
	one := []byte(`{"z":[{"n":9007199254740993}],"text":"\ud83d\ude80","a":1}`)
	two := []byte(` {"a":1,"text":"🚀","z":[{"n":9007199254740993}]} `)
	a, err := CanonicalJSON(one)
	if err != nil {
		t.Fatal(err)
	}
	b, err := CanonicalJSON(two)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) || !strings.Contains(string(a), "9007199254740993") {
		t.Fatalf("canonical arguments changed identity: %s / %s", a, b)
	}
	first, err := ArgumentsHash(one)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ArgumentsHash(two)
	if err != nil || first != second {
		t.Fatal("equivalent object order changed proof")
	}
	for _, raw := range []string{`{"secret-sentinel":1,"secret-sentinel":2}`, `{"x":{"a":1,"a":2}}`, `{"text":"\ud800"}`, `{"x":1} {"x":2}`, `[1,2]`, ``} {
		if _, err := ArgumentsHash([]byte(raw)); err == nil || strings.Contains(err.Error(), "secret-sentinel") {
			t.Fatalf("invalid or secret-bearing error: %v", err)
		}
	}
	if _, err := CanonicalJSON([]byte(`{"TEXT":1,"text":2}`)); err != nil {
		t.Fatal("canonical JSON imposed unrelated client key-case rules")
	}
}
