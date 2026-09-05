package pushreg

import "testing"

func TestRegistrationProofMessageBindsIdempotencyKeyAndExactBody(t *testing.T) {
	const idem = "AAAAAAAAAAAAAAAAAAAAAA"
	want := "swarm-pg-register-v1|" + idem + "|Iw2DWNyOiJC0xY3utikS7i8gNXrpKlzIYbmOaP4xrLU"
	if got := string(RegistrationProofMessage(idem, []byte("body"))); got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
	if got := string(RegistrationProofMessage(idem, []byte("body!"))); got == want {
		t.Fatal("body mutation did not change proof message")
	}
	if got := string(RegistrationProofMessage("BBBBBBBBBBBBBBBBBBBBBB", []byte("body"))); got == want {
		t.Fatal("idempotency-key mutation did not change proof message")
	}
}
