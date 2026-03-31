package credproxy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func testEncryptor(t *testing.T) *Encryptor {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	enc, err := NewEncryptor(hex.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	return enc
}

func TestCredentialStore_UpsertAndGet(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	enc := testEncryptor(t)
	store, err := NewCredentialStore(db, enc)
	if err != nil {
		t.Fatal(err)
	}

	cred := &Credential{
		GroupJID: "discord:123",
		Provider: "openai",
		APIKey:   "sk-test-key",
	}

	mock.ExpectExec("REPLACE INTO credentials").
		WithArgs("discord:123", "openai", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := store.UpsertCredential(context.Background(), cred); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestCredentialStore_Delete(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	enc := testEncryptor(t)
	store, err := NewCredentialStore(db, enc)
	if err != nil {
		t.Fatal(err)
	}

	mock.ExpectExec("DELETE FROM credentials WHERE group_jid").
		WithArgs("discord:123").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := store.DeleteCredential(context.Background(), "discord:123"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
