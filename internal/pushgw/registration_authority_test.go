package pushgw

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
)

func authorityRepositories(t *testing.T) map[string]Repository {
	t.Helper()
	out := map[string]Repository{"memory": NewMemoryRepository()}
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		return out
	}
	client, err := firestore.NewClient(context.Background(), "demo-swarm-push-probe")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	repo, err := NewFirestoreRepository(client, fmt.Sprintf("go-registration-authority-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	out["firestore"] = repo
	return out
}

func completeAuthority(t *testing.T, repo Repository, now time.Time) {
	t.Helper()
	_, won, _, _, err := repo.claimRegistration(context.Background(), "registration", "logical", "installation", "lease", now)
	if err != nil || !won {
		t.Fatalf("claim won=%v err=%v", won, err)
	}
	_, completed, err := repo.completeRegistration(context.Background(), "registration", "logical", "lease", installationRecord{RegistrationID: "registration", LastActiveMs: now.UnixMilli()}, now)
	if err != nil || !completed {
		t.Fatalf("complete=%v err=%v", completed, err)
	}
}

func setAuthorityLastActive(t *testing.T, repo Repository, when time.Time) {
	t.Helper()
	switch r := repo.(type) {
	case *memoryRepository:
		r.mu.Lock()
		rec := r.installations["installation"]
		rec.LastActiveMs = when.UnixMilli()
		r.installations["installation"] = rec
		r.mu.Unlock()
	case *firestorePersistence:
		if _, err := r.col("installations").Doc("installation").Update(context.Background(), []firestore.Update{{Path: "last_active_ms", Value: when.UnixMilli()}}); err != nil {
			t.Fatal(err)
		}
	}
}

func deleteAuthorityInstallation(t *testing.T, repo Repository) {
	t.Helper()
	switch r := repo.(type) {
	case *memoryRepository:
		r.mu.Lock()
		delete(r.installations, "installation")
		r.mu.Unlock()
	case *firestorePersistence:
		if _, err := r.col("installations").Doc("installation").Delete(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

func seedRegistration(t *testing.T, repo Repository, rec registrationRecord) {
	t.Helper()
	switch r := repo.(type) {
	case *memoryRepository:
		r.mu.Lock()
		r.regs["registration"] = rec
		r.mu.Unlock()
	case *firestorePersistence:
		if _, err := r.col("registration_attempts").Doc("registration").Set(context.Background(), rec); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRegistrationAuthority_RejectsUnknownDigestRevision(t *testing.T) {
	for name, repo := range authorityRepositories(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Unix(1_900_000_000, 0).UTC()
			seedRegistration(t, repo, registrationRecord{BodyDigest: "logical", DigestRevision: 1, InstallationID: "candidate", State: "pending", ExpiresAtMs: now.Add(time.Minute).UnixMilli(), LeaseID: "lease", LeaseUntilMs: now.Add(time.Minute).UnixMilli()})
			if _, _, _, err := repo.lookupRegistration(context.Background(), "registration", "logical", now); err == nil {
				t.Fatal("lookup accepted unknown digest revision")
			}
			if _, _, _, _, err := repo.claimRegistration(context.Background(), "registration", "logical", "other", "other-lease", now); err == nil {
				t.Fatal("claim accepted unknown digest revision")
			}
			if _, _, err := repo.completeRegistration(context.Background(), "registration", "logical", "lease", installationRecord{}, now); err == nil {
				t.Fatal("complete accepted unknown digest revision")
			}
		})
	}
}

func TestRegistrationAuthority_StaleLeaseCannotComplete(t *testing.T) {
	for name, repo := range authorityRepositories(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Unix(1_900_000_000, 0).UTC()
			_, won, _, _, err := repo.claimRegistration(context.Background(), "registration", "logical", "candidate", "lease", now)
			if err != nil || !won {
				t.Fatalf("claim won=%v err=%v", won, err)
			}
			result, completed, err := repo.completeRegistration(context.Background(), "registration", "logical", "lease", installationRecord{RegistrationID: "registration"}, now.Add(registrationLease))
			if err != nil || completed || result.InstallationID != "" {
				t.Fatalf("stale completion result=%+v completed=%v err=%v", result, completed, err)
			}
		})
	}
}

func TestRegistrationAuthority_RetentionRejectsUnknownRevision(t *testing.T) {
	for _, state := range []string{"pending", "completed"} {
		t.Run(state, func(t *testing.T) {
			for name, repo := range authorityRepositories(t) {
				t.Run(name, func(t *testing.T) {
					now := time.Unix(1_900_000_000, 0).UTC()
					rec := registrationRecord{BodyDigest: "logical", DigestRevision: 1, InstallationID: "installation", State: state}
					if state == "completed" {
						completeAuthority(t, repo, now)
						setAuthorityLastActive(t, repo, now.Add(-installationWindow))
					} else {
						rec.ExpiresAtMs = now.Add(-time.Second).UnixMilli()
					}
					seedRegistration(t, repo, rec)
					if err := repo.runRetention(context.Background(), now); err == nil {
						t.Fatal("retention accepted an incompatible registration revision")
					}
					if _, _, _, err := repo.lookupRegistration(context.Background(), "registration", "logical", now); err == nil {
						t.Fatal("retention deleted the incompatible receipt")
					}
					if state == "completed" {
						if _, found, err := repo.getInstallation(context.Background(), "installation"); err != nil || !found {
							t.Fatalf("retention lost linked installation: found=%v err=%v", found, err)
						}
					}
				})
			}
		})
	}
}

func TestRegistrationAuthority_CorruptCompletedHalfFailsClosed(t *testing.T) {
	for name, repo := range authorityRepositories(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Unix(1_900_000_000, 0).UTC()
			completeAuthority(t, repo, now)
			deleteAuthorityInstallation(t, repo)
			if _, found, mismatch, err := repo.lookupRegistration(context.Background(), "registration", "logical", now); err == nil || found || mismatch {
				t.Fatalf("corrupt authority found=%v mismatch=%v err=%v", found, mismatch, err)
			}
		})
	}
}

func TestRegistrationAuthority_RetentionDeletesExactCompletedHalf(t *testing.T) {
	for name, repo := range authorityRepositories(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Unix(1_900_000_000, 0).UTC()
			completeAuthority(t, repo, now)
			setAuthorityLastActive(t, repo, now.Add(-installationWindow))
			if err := repo.runRetention(context.Background(), now); err != nil {
				t.Fatal(err)
			}
			if _, found, err := repo.getInstallation(context.Background(), "installation"); err != nil || found {
				t.Fatalf("installation found=%v err=%v", found, err)
			}
			if _, found, _, err := repo.lookupRegistration(context.Background(), "registration", "logical", now); err != nil || found {
				t.Fatalf("authority found=%v err=%v", found, err)
			}
		})
	}
}

func TestRegistrationAuthority_ReplayTouchesBeforeRetention(t *testing.T) {
	for name, repo := range authorityRepositories(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Unix(1_900_000_000, 0).UTC()
			completeAuthority(t, repo, now)
			setAuthorityLastActive(t, repo, now.Add(-installationWindow+time.Second))
			result, found, mismatch, err := repo.lookupRegistration(context.Background(), "registration", "logical", now)
			if err != nil || !found || mismatch || result.InstallationID != "installation" {
				t.Fatalf("replay result=%+v found=%v mismatch=%v err=%v", result, found, mismatch, err)
			}
			if err := repo.runRetention(context.Background(), now); err != nil {
				t.Fatal(err)
			}
			if _, found, err := repo.getInstallation(context.Background(), "installation"); err != nil || !found {
				t.Fatalf("touched installation found=%v err=%v", found, err)
			}
		})
	}
}
