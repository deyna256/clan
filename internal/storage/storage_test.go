package storage_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/deyna256/clan/internal/accesskey"
	"github.com/deyna256/clan/internal/account"
	"github.com/deyna256/clan/internal/concurrency"
	"github.com/deyna256/clan/internal/credentialcipher"
	"github.com/deyna256/clan/internal/storage"
)

const (
	accountAccessToken  = "account-access-token"
	accountRefreshToken = "account-refresh-token"
	fixtureKeyOne       = "clan_AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"
	fixtureKeyTwo       = "clan_BAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"
)

func TestStoreRoundTripsAccountsAndKeysAfterReopen(t *testing.T) {
	requireStorage(t)
	path := filepath.Join(t.TempDir(), "clan.db")
	cipher := newCipher(t)
	first := openStore(t, path, cipher)

	accountOne := newAccountWithCredentials(t, "account-1", "Primary", account.OAuthCredentials{
		ChatGPTAccountID: "provider-shared",
		AccessToken:      "access-one",
		RefreshToken:     "refresh-one",
	})
	accountTwo := newAccountWithCredentials(
		t,
		"account-2",
		"Secondary",
		account.OAuthCredentials{
			ChatGPTAccountID: "provider-shared",
			AccessToken:      "access-two",
			RefreshToken:     "refresh-two",
			ExpiresAt:        time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC),
		},
	)
	if err := first.CreateAccount(context.Background(), accountTwo, true); err != nil {
		t.Fatal(err)
	}
	if err := first.CreateAccount(context.Background(), accountOne, false); err != nil {
		t.Fatal(err)
	}

	keyOne, hashOne := newKey(t, "key-1", "Worker", true, fixtureKeyOne)
	keyTwo, hashTwo := newKey(t, "key-2", "Read only", false, fixtureKeyTwo)
	if err := first.CreateAccessKey(context.Background(), keyTwo, hashTwo, 0); err != nil {
		t.Fatal(err)
	}
	if err := first.CreateAccessKey(context.Background(), keyOne, hashOne, concurrency.Unlimited); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	databaseBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(databaseBytes, []byte(fixtureKeyOne)) || bytes.Contains(databaseBytes, []byte(fixtureKeyTwo)) {
		t.Fatal("database contains a raw client key")
	}

	second := openStore(t, path, cipher)
	accounts, err := second.ListAccounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 2 {
		t.Fatalf("ListAccounts() returned %d records, want 2", len(accounts))
	}
	assertAccountRecord(t, accounts[0], accountOne, false)
	assertAccountRecord(t, accounts[1], accountTwo, true)

	keys, err := second.ListAccessKeys(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("ListAccessKeys() returned %d records, want 2", len(keys))
	}
	assertKeyRecord(t, keys[0], keyOne, hashOne, concurrency.Unlimited)
	assertKeyRecord(t, keys[1], keyTwo, hashTwo, 0)

	found, err := second.FindAccessKeyByHash(context.Background(), hashTwo)
	if err != nil {
		t.Fatal(err)
	}
	assertKeyRecord(t, found, keyTwo, hashTwo, 0)
}

func TestDuplicateAccountIDPreservesOriginalRecord(t *testing.T) {
	requireStorage(t)
	store := openStore(t, filepath.Join(t.TempDir(), "clan.db"), newCipher(t))
	original := newAccount(t, "account", "Original")
	if err := store.CreateAccount(t.Context(), original, true); err != nil {
		t.Fatal(err)
	}
	duplicate := newAccountWithCredentials(t, "account", "Changed", account.OAuthCredentials{
		ChatGPTAccountID: "provider-other", AccessToken: "new-access", RefreshToken: "new-refresh",
	})

	err := store.CreateAccount(t.Context(), duplicate, false)

	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("duplicate account ID error = %v, want ErrConflict", err)
	}
	got, err := store.GetAccount(t.Context(), "account")
	if err != nil {
		t.Fatal(err)
	}
	assertAccountRecord(t, got, original, true)
}

func TestDuplicateAccessKeyIDPreservesOriginalRecord(t *testing.T) {
	requireStorage(t)
	store := openStore(t, filepath.Join(t.TempDir(), "clan.db"), newCipher(t))
	key, hash := newKey(t, "key", "Original", true, fixtureKeyOne)
	if err := store.CreateAccessKey(t.Context(), key, hash, 2); err != nil {
		t.Fatal(err)
	}
	duplicate, otherHash := newKey(t, "key", "Changed", false, fixtureKeyTwo)

	err := store.CreateAccessKey(t.Context(), duplicate, otherHash, 9)

	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("duplicate key ID error = %v, want ErrConflict", err)
	}
	if _, err := store.FindAccessKeyByHash(t.Context(), otherHash); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("hash from rejected duplicate key = %v, want ErrNotFound", err)
	}
	got, err := store.FindAccessKeyByHash(t.Context(), hash)
	if err != nil {
		t.Fatal(err)
	}
	assertKeyRecord(t, got, key, hash, 2)
}

func TestDuplicateAccessKeyHashPreservesOriginalRecord(t *testing.T) {
	requireStorage(t)
	store := openStore(t, filepath.Join(t.TempDir(), "clan.db"), newCipher(t))
	key, hash := newKey(t, "key", "Original", true, fixtureKeyOne)
	if err := store.CreateAccessKey(t.Context(), key, hash, 2); err != nil {
		t.Fatal(err)
	}
	other, _ := newKey(t, "other", "Changed", false, fixtureKeyTwo)

	err := store.CreateAccessKey(t.Context(), other, hash, 9)

	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("duplicate key hash error = %v, want ErrConflict", err)
	}
	got, err := store.FindAccessKeyByHash(t.Context(), hash)
	if err != nil {
		t.Fatal(err)
	}
	assertKeyRecord(t, got, key, hash, 2)
}

func TestInvalidConcurrencyUpdatePreservesAccessKey(t *testing.T) {
	requireStorage(t)
	store := openStore(t, filepath.Join(t.TempDir(), "clan.db"), newCipher(t))
	key, hash := newKey(t, "key", "Original", true, fixtureKeyOne)
	if err := store.CreateAccessKey(t.Context(), key, hash, 2); err != nil {
		t.Fatal(err)
	}

	err := store.UpdateAccessKeyConcurrency(t.Context(), "key", -2)

	if !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("invalid concurrency error = %v, want ErrInvalid", err)
	}
	got, err := store.FindAccessKeyByHash(t.Context(), hash)
	if err != nil {
		t.Fatal(err)
	}
	assertKeyRecord(t, got, key, hash, 2)
}

func TestZeroAccountWritePreservesExistingAccount(t *testing.T) {
	requireStorage(t)
	store := openStore(t, filepath.Join(t.TempDir(), "clan.db"), newCipher(t))
	original := newAccount(t, "account", "Original")
	if err := store.CreateAccount(t.Context(), original, true); err != nil {
		t.Fatal(err)
	}

	err := store.CreateAccount(t.Context(), account.Account{}, true)

	if !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("zero account error = %v, want ErrInvalid", err)
	}
	got, err := store.GetAccount(t.Context(), "account")
	if err != nil {
		t.Fatal(err)
	}
	assertAccountRecord(t, got, original, true)
}

func TestCredentialReplacementFailurePreservesRecord(t *testing.T) {
	requireStorage(t)
	path := filepath.Join(t.TempDir(), "clan.db")
	store := openStore(t, path, newCipher(t))
	original := newAccount(t, "account", "Original")
	if err := store.CreateAccount(context.Background(), original, true); err != nil {
		t.Fatal(err)
	}
	previous, err := store.GetAccount(context.Background(), "account")
	if err != nil {
		t.Fatal(err)
	}
	db := openRaw(t, path)
	if _, err := db.Exec(`
		CREATE TRIGGER reject_credential_update
		BEFORE UPDATE OF credentials ON accounts
		BEGIN
			SELECT RAISE(ABORT, 'credential update rejected');
		END`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceAccountCredentialsIfUnchanged(context.Background(), previous, account.OAuthCredentials{
		ChatGPTAccountID: "provider-failed",
		AccessToken:      "access-failed",
	}); err == nil {
		t.Fatal("triggered credential replacement succeeded")
	}
	if got, err := store.GetAccount(context.Background(), "account"); err != nil {
		t.Fatal(err)
	} else {
		assertAccountRecord(t, got, original, true)
	}
}

func TestCredentialReplacementCancellationPreservesRecord(t *testing.T) {
	requireStorage(t)
	path := filepath.Join(t.TempDir(), "clan.db")
	store := openStore(t, path, newCipher(t))
	original := newAccount(t, "account", "Original")
	if err := store.CreateAccount(context.Background(), original, true); err != nil {
		t.Fatal(err)
	}
	previous, err := store.GetAccount(context.Background(), "account")
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	err = store.ReplaceAccountCredentialsIfUnchanged(cancelled, previous, account.OAuthCredentials{
		ChatGPTAccountID: "provider-cancelled",
		AccessToken:      "access-cancelled",
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled credential replacement error = %v, want context.Canceled", err)
	}
	if got, err := store.GetAccount(context.Background(), "account"); err != nil {
		t.Fatal(err)
	} else {
		assertAccountRecord(t, got, original, true)
	}
}

func TestConditionalCredentialReplacementPreservesEnabledStateAndRejectsDeletedRows(t *testing.T) {
	requireStorage(t)
	path := filepath.Join(t.TempDir(), "clan.db")
	store := openStore(t, path, newCipher(t))
	original := newAccount(t, "account", "Original")
	if err := store.CreateAccount(context.Background(), original, true); err != nil {
		t.Fatal(err)
	}
	previous, err := store.GetAccount(context.Background(), "account")
	if err != nil {
		t.Fatal(err)
	}
	updated := account.OAuthCredentials{
		ChatGPTAccountID: "provider-updated",
		AccessToken:      "access-updated",
		RefreshToken:     "refresh-updated",
		ExpiresAt:        time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC),
	}
	if err := store.ReplaceAccountCredentialsIfUnchanged(context.Background(), previous, updated); err != nil {
		t.Fatal(err)
	}
	record, err := store.GetAccount(context.Background(), "account")
	if err != nil {
		t.Fatal(err)
	}
	want := newAccountWithCredentials(t, "account", "Original", updated)
	assertAccountRecord(t, record, want, true)

	if err := store.DeleteAccount(context.Background(), "account"); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceAccountCredentialsIfUnchanged(context.Background(), previous, updated); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("replacement after delete error = %v, want ErrConflict", err)
	}
	if err := store.DeleteAccount(context.Background(), "account"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("second delete error = %v, want ErrNotFound", err)
	}
}

func TestCredentialReplacementDeadlineWhileDatabaseIsWriteLocked(t *testing.T) {
	requireStorage(t)
	path := filepath.Join(t.TempDir(), "clan.db")
	store := openStore(t, path, newCipher(t))
	original := newAccount(t, "account", "Original")
	if err := store.CreateAccount(context.Background(), original, true); err != nil {
		t.Fatal(err)
	}
	previous, err := store.GetAccount(context.Background(), "account")
	if err != nil {
		t.Fatal(err)
	}
	db := openRaw(t, path)
	lock, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := lock.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			t.Error(err)
		}
	})
	if _, err := lock.Exec(`UPDATE accounts SET name = name WHERE id = 'account'`); err != nil {
		t.Fatal(err)
	}
	credentials := account.OAuthCredentials{
		ChatGPTAccountID: "provider-updated",
		AccessToken:      "access-updated",
		RefreshToken:     "refresh-updated",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	started := time.Now()
	err = store.ReplaceAccountCredentialsIfUnchanged(ctx, previous, credentials)

	t.Logf("write with 100ms deadline returned after %s", time.Since(started))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("locked credential replacement error = %v, want context.DeadlineExceeded", err)
	}
	if err := lock.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openStore(t, path, newCipher(t))
	got, err := reopened.GetAccount(context.Background(), "account")
	if err != nil {
		t.Fatal(err)
	}
	assertAccountRecord(t, got, original, true)
}

func TestDisableAndRevokeAreRepeatableAndIndependentOfOtherSettings(t *testing.T) {
	requireStorage(t)
	store := openStore(t, filepath.Join(t.TempDir(), "clan.db"), newCipher(t))
	value := newAccount(t, "account", "Primary")
	if err := store.CreateAccount(context.Background(), value, true); err != nil {
		t.Fatal(err)
	}
	if err := store.DisableAccount(context.Background(), "account"); err != nil {
		t.Fatal(err)
	}
	if err := store.DisableAccount(context.Background(), "account"); err != nil {
		t.Fatal(err)
	}
	accountRecord, err := store.GetAccount(context.Background(), "account")
	if err != nil {
		t.Fatal(err)
	}
	assertAccountRecord(t, accountRecord, value, false)

	key, hash := newKey(t, "key", "Primary", true, fixtureKeyOne)
	if err := store.CreateAccessKey(context.Background(), key, hash, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.RevokeAccessKey(context.Background(), "key"); err != nil {
		t.Fatal(err)
	}
	if err := store.RevokeAccessKey(context.Background(), "key"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateAccessKeyConcurrency(context.Background(), "key", 7); err != nil {
		t.Fatal(err)
	}
	found, err := store.FindAccessKeyByHash(context.Background(), hash)
	if err != nil {
		t.Fatal(err)
	}
	if found.Key.Enabled() || found.ConcurrencyLimit != 7 || found.VerificationHash != hash {
		t.Errorf("key updates lost independent fields: enabled=%t limit=%d hash=%x", found.Key.Enabled(), found.ConcurrencyLimit, found.VerificationHash)
	}
}

func TestConcurrentIndependentKeyUpdatesPersist(t *testing.T) {
	requireStorage(t)
	path := filepath.Join(t.TempDir(), "clan.db")
	store := openStore(t, path, newCipher(t))
	key, hash := newKey(t, "key", "One", true, fixtureKeyOne)
	if err := store.CreateAccessKey(context.Background(), key, hash, 1); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		<-start
		results <- store.UpdateAccessKeyConcurrency(context.Background(), "key", 9)
	}()
	go func() {
		defer workers.Done()
		<-start
		results <- store.RevokeAccessKey(context.Background(), "key")
	}()
	close(start)
	workers.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}

	first, err := store.FindAccessKeyByHash(context.Background(), hash)
	if err != nil {
		t.Fatal(err)
	}
	if first.Key.Enabled() || first.ConcurrencyLimit != 9 || first.VerificationHash != hash || first.Key.Identity() != key.Identity() {
		t.Errorf("concurrent updates = enabled %t, limit %d, hash %x, identity %+v", first.Key.Enabled(), first.ConcurrencyLimit, first.VerificationHash, first.Key.Identity())
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openStore(t, path, newCipher(t))
	second, err := reopened.FindAccessKeyByHash(context.Background(), hash)
	if err != nil {
		t.Fatal(err)
	}
	if second.Key.Enabled() || second.ConcurrencyLimit != 9 || second.VerificationHash != hash || second.Key.Identity() != key.Identity() {
		t.Errorf("reopened concurrent updates = enabled %t, limit %d, hash %x, identity %+v", second.Key.Enabled(), second.ConcurrencyLimit, second.VerificationHash, second.Key.Identity())
	}
}

func TestOpenRejectsWrongOrCorruptCredentialRecordsWithoutWriting(t *testing.T) {
	requireStorage(t)
	path := filepath.Join(t.TempDir(), "clan.db")
	cipher := newCipher(t)
	store := openStore(t, path, cipher)
	value := newAccountWithCredentials(t, "account", "Primary", account.OAuthCredentials{
		ChatGPTAccountID: "provider",
		AccessToken:      accountAccessToken,
		RefreshToken:     accountRefreshToken,
	})
	if err := store.CreateAccount(context.Background(), value, true); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	beforeWrongKey := readAccountBlob(t, path, "account")

	wrongCipher, err := credentialcipher.New(bytes.Repeat([]byte{0x99}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.Open(context.Background(), path, wrongCipher); !errors.Is(err, storage.ErrCorrupt) {
		t.Fatalf("Open() with wrong cipher error = %v, want ErrCorrupt", err)
	}
	afterWrongKey := readAccountBlob(t, path, "account")
	if !bytes.Equal(afterWrongKey, beforeWrongKey) {
		t.Fatal("wrong-key Open() changed the stored ciphertext")
	}
	if bytes.Contains(afterWrongKey, []byte(accountAccessToken)) || bytes.Contains(afterWrongKey, []byte(accountRefreshToken)) {
		t.Fatal("database contains raw OAuth credentials")
	}

	corrupt := []byte("corrupt")
	db := openRaw(t, path)
	if _, err := db.Exec(`UPDATE accounts SET credentials = ? WHERE id = ?`, corrupt, "account"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.Open(context.Background(), path, cipher); !errors.Is(err, storage.ErrCorrupt) {
		t.Fatalf("Open() with corrupt cipher text error = %v, want ErrCorrupt", err)
	}
	if got := readAccountBlob(t, path, "account"); !bytes.Equal(got, corrupt) {
		t.Fatal("corrupt-record Open() changed the stored ciphertext")
	}
}

func TestOpenRejectsSwappedCredentialBlobs(t *testing.T) {
	requireStorage(t)
	path := filepath.Join(t.TempDir(), "clan.db")
	cipher := newCipher(t)
	store := openStore(t, path, cipher)
	if err := store.CreateAccount(context.Background(), newAccount(t, "one", "One"), true); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAccount(context.Background(), newAccount(t, "two", "Two"), true); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	one := readAccountBlob(t, path, "one")
	two := readAccountBlob(t, path, "two")
	db := openRaw(t, path)
	if _, err := db.Exec(`UPDATE accounts SET credentials = ? WHERE id = ?`, two, "one"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE accounts SET credentials = ? WHERE id = ?`, one, "two"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.Open(context.Background(), path, cipher); !errors.Is(err, storage.ErrCorrupt) {
		t.Fatalf("Open() with swapped credential blobs error = %v, want ErrCorrupt", err)
	}
}

func TestOpenRejectsUnversionedSchemaWithoutChangingData(t *testing.T) {
	requireStorage(t)
	path := filepath.Join(t.TempDir(), "conflict.db")
	db := openRaw(t, path)
	if _, err := db.Exec(`CREATE TABLE unrelated (value TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO unrelated (value) VALUES ('keep')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	_, err := storage.Open(t.Context(), path, newCipher(t))

	if err == nil {
		t.Fatal("Open() adopted a conflicting unversioned table")
	}
	db = openRaw(t, path)
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 0 {
		t.Fatalf("conflicting database user_version = %d, want 0", version)
	}
	var value string
	if err := db.QueryRow(`SELECT value FROM unrelated`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "keep" {
		t.Fatalf("conflicting database value = %q, want keep", value)
	}
}

func TestOpenRejectsTableResemblingSQLiteMetadata(t *testing.T) {
	requireStorage(t)
	path := filepath.Join(t.TempDir(), "sqlite-name.db")
	db := openRaw(t, path)
	if _, err := db.Exec(`CREATE TABLE sqliteXexample (value TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	_, err := storage.Open(t.Context(), path, newCipher(t))

	if err == nil {
		t.Fatal("Open() adopted a table whose name only resembles SQLite metadata")
	}
}

func TestOpenRejectsNewerSchemaWithoutChangingCredentials(t *testing.T) {
	requireStorage(t)
	path := filepath.Join(t.TempDir(), "newer.db")
	store := openStore(t, path, newCipher(t))
	if err := store.CreateAccount(t.Context(), newAccount(t, "account", "Primary"), true); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	before := readAccountBlob(t, path, "account")
	db := openRaw(t, path)
	if _, err := db.Exec(`PRAGMA user_version = 2`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	_, err := storage.Open(t.Context(), path, newCipher(t))

	if err == nil {
		t.Fatal("Open() accepted a newer schema version")
	}
	if got := readAccountBlob(t, path, "account"); !bytes.Equal(got, before) {
		t.Fatal("newer schema rejection changed the existing account")
	}
}

func TestPathIsUsedLiterally(t *testing.T) {
	requireStorage(t)
	path := filepath.Join(t.TempDir(), "question?mark.db")
	store := openStore(t, path, newCipher(t))
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database path was not used literally: %v", err)
	}
}

func requireStorage(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("SQLite storage tests are integration tests")
	}
}

func openStore(t *testing.T, path string, cipher *credentialcipher.Cipher) *storage.Store {
	t.Helper()
	store, err := storage.Open(context.Background(), path, cipher)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return store
}

func openRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	return db
}

func newCipher(t *testing.T) *credentialcipher.Cipher {
	t.Helper()
	cipher, err := credentialcipher.New(bytes.Repeat([]byte{0x2a}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return cipher
}

func newAccount(t *testing.T, id account.ID, name string) account.Account {
	t.Helper()
	return newAccountWithCredentials(
		t,
		id,
		name,
		account.OAuthCredentials{
			ChatGPTAccountID: "provider-" + string(id),
			AccessToken:      "access-" + string(id),
			RefreshToken:     "refresh-" + string(id),
		},
	)
}

func newAccountWithCredentials(
	t *testing.T,
	id account.ID,
	name string,
	credentials account.OAuthCredentials,
) account.Account {
	t.Helper()
	value, err := account.New(
		account.Identity{ID: id, Name: name},
		credentials,
	)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func newKey(
	t *testing.T,
	id accesskey.ID,
	name string,
	enabled bool,
	raw string,
) (accesskey.AccessKey, accesskey.VerificationHash) {
	t.Helper()
	key, err := accesskey.New(accesskey.Identity{ID: id, Name: name}, enabled)
	if err != nil {
		t.Fatal(err)
	}
	hash, ok := accesskey.Hash(raw)
	if !ok {
		t.Fatalf("fixture key %q is invalid", raw)
	}
	return key, hash
}

func assertAccountRecord(t *testing.T, got storage.AccountRecord, want account.Account, enabled bool) {
	t.Helper()
	if got.Enabled != enabled {
		t.Errorf("account enabled = %t, want %t", got.Enabled, enabled)
	}
	if got.Account.Identity() != want.Identity() {
		t.Errorf("account identity = %+v, want %+v", got.Account.Identity(), want.Identity())
	}
	if got.Account.Credentials() != want.Credentials() {
		t.Errorf("account credentials changed across storage round trip")
	}
}

func assertKeyRecord(
	t *testing.T,
	got storage.AccessKeyRecord,
	want accesskey.AccessKey,
	wantHash accesskey.VerificationHash,
	wantLimit int,
) {
	t.Helper()
	if got.Key.Identity() != want.Identity() || got.Key.Enabled() != want.Enabled() {
		t.Errorf("key = (%+v, %t), want (%+v, %t)", got.Key.Identity(), got.Key.Enabled(), want.Identity(), want.Enabled())
	}
	if got.VerificationHash != wantHash || got.ConcurrencyLimit != wantLimit {
		t.Errorf("key settings = (%x, %d), want (%x, %d)", got.VerificationHash, got.ConcurrencyLimit, wantHash, wantLimit)
	}
}

func readAccountBlob(t *testing.T, path string, id account.ID) []byte {
	t.Helper()
	db := openRaw(t, path)
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	var blob []byte
	if err := db.QueryRow(`SELECT credentials FROM accounts WHERE id = ?`, string(id)).Scan(&blob); err != nil {
		t.Fatal(err)
	}
	return blob
}
