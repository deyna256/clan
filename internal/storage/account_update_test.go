package storage_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/deyna256/clan/internal/account"
	"github.com/deyna256/clan/internal/storage"
)

func TestEnableAccountPreservesCredentialsAndInvalidatesOldWrites(t *testing.T) {
	s, previous := credentialReplacementFixture(t)
	if err := s.DisableAccount(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	updated := newAccount(t, "two", "Replacement").Credentials()

	if err := s.EnableAccount(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	err := s.ReplaceAccountCredentialsIfUnchanged(t.Context(), previous, updated)

	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("write after disable and enable = %v, want ErrConflict", err)
	}
	got, err := s.GetAccount(t.Context(), "one")
	if err != nil {
		t.Fatal(err)
	}
	assertAccountRecord(t, got, previous.Account, true)
	if err := s.EnableAccount(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceAccountCredentialsIfUnchanged(t.Context(), got, updated); err != nil {
		t.Fatalf("repeated enable invalidated current credentials: %v", err)
	}
}

func TestEnableAccountRejectsInvalidMissingAndCanceledRequests(t *testing.T) {
	s, previous := credentialReplacementFixture(t)
	if err := s.DisableAccount(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tt := range []struct {
		name string
		ctx  context.Context
		id   account.ID
		want error
	}{
		{name: "invalid ID", ctx: t.Context(), want: storage.ErrInvalid},
		{name: "missing account", ctx: t.Context(), id: "missing", want: storage.ErrNotFound},
		{name: "canceled request", ctx: canceled, id: "one", want: context.Canceled},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := s.EnableAccount(tt.ctx, tt.id)

			if !errors.Is(err, tt.want) {
				t.Fatalf("EnableAccount = %v, want %v", err, tt.want)
			}
		})
	}
	got, err := s.GetAccount(t.Context(), "one")
	if err != nil {
		t.Fatal(err)
	}
	assertAccountRecord(t, got, previous.Account, false)
}

func TestConditionalCredentialReplacementUsesListedSnapshotOnce(t *testing.T) {
	requireStorage(t)
	s := openStore(t, filepath.Join(t.TempDir(), "clan.db"), newCipher(t))
	original := newAccount(t, "one", "Original")
	createAccountRecord(t, s, original)
	records, err := s.ListAccounts(t.Context())
	if err != nil || len(records) != 1 {
		t.Fatalf("ListAccounts: %v, count %d", err, len(records))
	}
	updated := newAccount(t, "two", "Replacement").Credentials()

	err = s.ReplaceAccountCredentialsIfUnchanged(t.Context(), records[0], updated)

	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAccount(t.Context(), "one")
	if err != nil {
		t.Fatal(err)
	}
	want := newAccountWithCredentials(t, "one", "Original", updated)
	assertAccountRecord(t, got, want, true)
	if err := s.ReplaceAccountCredentialsIfUnchanged(t.Context(), records[0], original.Credentials()); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("stale replacement = %v, want ErrConflict", err)
	}
}

func TestConditionalCredentialReplacementRejectsDisabledAccount(t *testing.T) {
	s, previous := credentialReplacementFixture(t)
	if err := s.DisableAccount(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	updated := newAccount(t, "two", "Replacement").Credentials()

	err := s.ReplaceAccountCredentialsIfUnchanged(t.Context(), previous, updated)

	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("replacement = %v, want ErrConflict", err)
	}
	got, err := s.GetAccount(t.Context(), "one")
	if err != nil {
		t.Fatal(err)
	}
	assertAccountRecord(t, got, previous.Account, false)
}

func TestConditionalCredentialReplacementRejectsDeletedAccount(t *testing.T) {
	s, previous := credentialReplacementFixture(t)
	if err := s.DeleteAccount(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	updated := newAccount(t, "two", "Replacement").Credentials()

	err := s.ReplaceAccountCredentialsIfUnchanged(t.Context(), previous, updated)

	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("replacement = %v, want ErrConflict", err)
	}
	if _, err := s.GetAccount(t.Context(), "one"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("deleted account read = %v, want ErrNotFound", err)
	}
}

func TestConditionalCredentialReplacementRejectsRewrittenCredentials(t *testing.T) {
	s, previous := credentialReplacementFixture(t)
	if err := s.ReplaceAccountCredentials(t.Context(), "one", previous.Account.Credentials()); err != nil {
		t.Fatal(err)
	}
	updated := newAccount(t, "two", "Replacement").Credentials()

	err := s.ReplaceAccountCredentialsIfUnchanged(t.Context(), previous, updated)

	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("replacement = %v, want ErrConflict", err)
	}
	got, err := s.GetAccount(t.Context(), "one")
	if err != nil {
		t.Fatal(err)
	}
	assertAccountRecord(t, got, previous.Account, true)
}

func TestConditionalCredentialReplacementRejectsRecreatedAccount(t *testing.T) {
	s, previous := credentialReplacementFixture(t)
	if err := s.DeleteAccount(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	createAccountRecord(t, s, previous.Account)
	updated := newAccount(t, "two", "Replacement").Credentials()

	err := s.ReplaceAccountCredentialsIfUnchanged(t.Context(), previous, updated)

	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("replacement = %v, want ErrConflict", err)
	}
	got, err := s.GetAccount(t.Context(), "one")
	if err != nil {
		t.Fatal(err)
	}
	assertAccountRecord(t, got, previous.Account, true)
}

func TestConcurrentConditionalCredentialReplacementsHaveOneWinner(t *testing.T) {
	requireStorage(t)
	s := openStore(t, filepath.Join(t.TempDir(), "clan.db"), newCipher(t))
	original := newAccount(t, "one", "Original")
	previous := createAccountRecord(t, s, original)
	updates := []account.OAuthCredentials{
		newAccount(t, "two", "First").Credentials(),
		newAccount(t, "three", "Second").Credentials(),
	}
	results := make([]error, len(updates))
	start := make(chan struct{})
	var workers sync.WaitGroup

	for i, credentials := range updates {
		workers.Go(func() {
			<-start
			results[i] = s.ReplaceAccountCredentialsIfUnchanged(t.Context(), previous, credentials)
		})
	}
	close(start)
	workers.Wait()

	winner := -1
	for i, err := range results {
		if errors.Is(err, storage.ErrConflict) {
			continue
		}
		if err != nil {
			t.Fatalf("write %d = %v, want success or ErrConflict", i, err)
		}
		if winner != -1 {
			t.Fatal("both conditional writes succeeded")
		}
		winner = i
	}
	if winner == -1 {
		t.Fatal("neither conditional write succeeded")
	}
	got, err := s.GetAccount(t.Context(), "one")
	if err != nil {
		t.Fatal(err)
	}
	want := newAccountWithCredentials(t, "one", "Original", updates[winner])
	assertAccountRecord(t, got, want, true)
}

func createAccountRecord(t *testing.T, s *storage.Store, value account.Account) storage.AccountRecord {
	t.Helper()
	if err := s.CreateAccount(t.Context(), value, true); err != nil {
		t.Fatal(err)
	}
	record, err := s.GetAccount(t.Context(), value.Identity().ID)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func credentialReplacementFixture(t *testing.T) (*storage.Store, storage.AccountRecord) {
	t.Helper()
	requireStorage(t)
	s := openStore(t, filepath.Join(t.TempDir(), "clan.db"), newCipher(t))
	return s, createAccountRecord(t, s, newAccount(t, "one", "Original"))
}
