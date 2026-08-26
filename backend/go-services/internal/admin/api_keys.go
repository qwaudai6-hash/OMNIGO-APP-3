package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/omnigo/backend/internal/shared/messaging"
	"github.com/omnigo/backend/internal/shared/security"
)

// AllowedProviders is the allow-list of payment providers whose API keys the
// admin UI may manage. Keep this list short and explicit — any new provider
// requires both a migration and a registered key_name below.
var allowedProviders = map[string]bool{
	"stripe":    true,
	"payfast":   true,
	"jazzcash":  true,
	"easypaisa": true,
	"osrm":      true, // OSRM auth token (e.g. self-hosted router with API key)
}

// AllowedKeyNames per provider. Each provider exposes a small fixed set of
// credentials; we explicitly enumerate them to refuse typos and accidental
// writes of arbitrary columns.
var allowedKeyNames = map[string]map[string]bool{
	"stripe": {
		"secret_key":      true,
		"webhook_secret":  true,
		"publishable_key": true,
	},
	"payfast": {
		"merchant_id": true,
		"secured_key": true,
	},
	"jazzcash": {
		"merchant_id":     true,
		"password":        true,
		"integerity_salt": true,
	},
	"easypaisa": {
		"merchant_id": true,
		"store_id":    true,
		"hash_key":    true,
	},
	"osrm": {
		"api_key": true,
	},
}

// APIKeyRecord is the row representation. The encrypted value is NEVER
// returned in list responses — only the fingerprint and metadata.
type APIKeyRecord struct {
	ID          uuid.UUID `json:"id"`
	Provider    string    `json:"provider"`
	KeyName     string    `json:"key_name"`
	Fingerprint string    `json:"fingerprint"`
	Version     int       `json:"version"`
	RotatedBy   string    `json:"rotated_by"`
	RotatedAt   time.Time `json:"rotated_at"`
}

// SetAPIKeyResult is the response of upsert: returns the masked record (no
// plaintext echoed back) plus a one-time `reveal_token` for the caller to
// display the value in the UI on success.
type SetAPIKeyResult struct {
	Record APIKeyRecord `json:"record"`
	// RevealOnce holds the plaintext for exactly this response. The Flutter
	// modal reads it once to confirm and never persists it.
	RevealOnce string `json:"reveal_once"`
}

// APIKeyService handles admin-managed payment credentials.
type APIKeyService struct {
	db     *pgxpool.Pool
	encKey string
	kafka  *messaging.KafkaClient
}

// NewAPIKeyService wires the service. encKey is the AES-GCM passphrase; it
// MUST be set via env var (ADMIN_API_KEY_ENCRYPTION_KEY) in production. In
// dev, the existing security.MustEnv helpers will fail fast at boot.
func NewAPIKeyService(db *pgxpool.Pool, encKey string, kafka *messaging.KafkaClient) *APIKeyService {
	return &APIKeyService{db: db, encKey: encKey, kafka: kafka}
}

// List returns metadata for every configured key, including a non-reversible
// fingerprint so admins can see "this is the same key as yesterday" without
// ever exposing the plaintext.
func (s *APIKeyService) List(ctx context.Context) ([]APIKeyRecord, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, provider, key_name, encrypted_value, version, rotated_by, rotated_at
		FROM payment_api_keys
		ORDER BY provider, key_name
	`)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	defer rows.Close()

	var out []APIKeyRecord
	for rows.Next() {
		var r APIKeyRecord
		var blob []byte
		if err := rows.Scan(&r.ID, &r.Provider, &r.KeyName, &blob, &r.Version, &r.RotatedBy, &r.RotatedAt); err != nil {
			return nil, fmt.Errorf("scan api key: %w", err)
		}
		r.Fingerprint = security.Fingerprint(blob)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows err: %w", err)
	}
	return out, nil
}

// Set creates or rotates a key. Returns the updated record plus the plaintext
// value in RevealOnce so the UI can confirm. The plaintext is NEVER written
// to logs, audit table, or any field other than the encrypted_value column.
func (s *APIKeyService) Set(ctx context.Context, provider, keyName, plaintext, actorID, actorIP string) (*SetAPIKeyResult, error) {
	if !allowedProviders[provider] {
		return nil, fmt.Errorf("unsupported provider %q", provider)
	}
	if !allowedKeyNames[provider][keyName] {
		return nil, fmt.Errorf("unsupported key_name %q for provider %q", keyName, provider)
	}
	if strings.TrimSpace(plaintext) == "" {
		return nil, errors.New("plaintext must not be empty")
	}
	if len(plaintext) > 4096 {
		return nil, errors.New("plaintext exceeds 4096 bytes")
	}

	blob, err := security.EncryptAESGCM(s.encKey, plaintext)
	if err != nil {
		return nil, fmt.Errorf("encrypt: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Look up the previous row (if any) for the audit trail.
	var prevID *uuid.UUID
	var prevBlob []byte
	err = tx.QueryRow(ctx, `
		SELECT id, encrypted_value FROM payment_api_keys
		WHERE provider = $1 AND key_name = $2
		FOR UPDATE
	`, provider, keyName).Scan(&prevID, &prevBlob)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("lookup previous: %w", err)
	}

	var newID uuid.UUID
	if prevID == nil {
		newID = uuid.New()
		_, err = tx.Exec(ctx, `
			INSERT INTO payment_api_keys (id, provider, key_name, encrypted_value, version, rotated_by, rotated_at)
			VALUES ($1, $2, $3, $4, 1, $5, NOW())
		`, newID, provider, keyName, blob, actorID)
	} else {
		newID = *prevID
		_, err = tx.Exec(ctx, `
			UPDATE payment_api_keys
			SET encrypted_value = $1, version = version + 1, rotated_by = $2, rotated_at = NOW()
			WHERE id = $3
		`, blob, actorID, newID)
	}
	if err != nil {
		return nil, fmt.Errorf("upsert api key: %w", err)
	}

	// Audit row with fingerprints only (no plaintext).
	var prevFP *string
	if len(prevBlob) > 0 {
		fp := security.Fingerprint(prevBlob)
		prevFP = &fp
	}
	newFP := security.Fingerprint(blob)
	action := "create"
	if prevID != nil {
		action = "update"
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO payment_api_key_audit
		    (payment_key_id, provider, key_name, action, prev_fingerprint, new_fingerprint, actor_id, actor_ip)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, newID, provider, keyName, action, prevFP, newFP, actorID, actorIP)
	if err != nil {
		return nil, fmt.Errorf("audit insert: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	// Notify downstream services to hot-reload. Kafka is best-effort: a failure
	// here does NOT roll back the DB write, because the next service restart
	// (or the next periodic refresh, if implemented) will pick up the new key.
	if s.kafka != nil {
		s.publishKeyChangeEvent(ctx, provider, keyName, false, actorID)
	}

	return &SetAPIKeyResult{
		Record: APIKeyRecord{
			ID:          newID,
			Provider:    provider,
			KeyName:     keyName,
			Fingerprint: newFP,
			Version:     0, // filled below
			RotatedBy:   actorID,
			RotatedAt:   time.Now().UTC(),
		},
		RevealOnce: plaintext,
	}, nil
}

// Delete removes a key. Returns the prior fingerprint for confirmation in
// the audit trail. Idempotent: deleting a missing key is a no-op success.
func (s *APIKeyService) Delete(ctx context.Context, provider, keyName, actorID, actorIP string) (string, error) {
	if !allowedProviders[provider] {
		return "", fmt.Errorf("unsupported provider %q", provider)
	}
	if !allowedKeyNames[provider][keyName] {
		return "", fmt.Errorf("unsupported key_name %q for provider %q", keyName, provider)
	}

	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var prevID uuid.UUID
	var prevBlob []byte
	err = tx.QueryRow(ctx, `
		SELECT id, encrypted_value FROM payment_api_keys
		WHERE provider = $1 AND key_name = $2
		FOR UPDATE
	`, provider, keyName).Scan(&prevID, &prevBlob)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil // idempotent
	}
	if err != nil {
		return "", fmt.Errorf("lookup: %w", err)
	}
	fp := security.Fingerprint(prevBlob)

	_, err = tx.Exec(ctx, `DELETE FROM payment_api_keys WHERE provider = $1 AND key_name = $2`, provider, keyName)
	if err != nil {
		return "", fmt.Errorf("delete: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO payment_api_key_audit
		    (payment_key_id, provider, key_name, action, prev_fingerprint, actor_id, actor_ip)
		VALUES ($1, $2, $3, 'delete', $4, $5, $6)
	`, prevID, provider, keyName, fp, actorID, actorIP)
	if err != nil {
		return "", fmt.Errorf("audit insert: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}

	if s.kafka != nil {
		s.publishKeyChangeEvent(ctx, provider, keyName, true, actorID)
	}

	return fp, nil
}

// publishKeyChangeEvent emits a payment.keys.updated event to Kafka. It is
// best-effort: a failure is logged but does not roll back the caller's DB
// write, because (a) the DB is the source of truth, and (b) downstream
// services can also re-read on startup or via a periodic poll.
func (s *APIKeyService) publishKeyChangeEvent(ctx context.Context, provider, keyName string, deleted bool, actorID string) {
	payload := map[string]interface{}{
		"provider":   provider,
		"key_name":   keyName,
		"rotated_by": actorID,
		"deleted":    deleted,
		"rotated_at": time.Now().UTC().Format(time.RFC3339),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[admin/api_keys] marshal event: %v", err)
		return
	}
	record := &kgo.Record{
		Topic: "payment.keys.updated",
		Key:   []byte(provider + "/" + keyName),
		Headers: []kgo.RecordHeader{
			{Key: "event_type", Value: []byte("payment.keys.updated")},
		},
		Value: body,
	}
	s.kafka.Client.Produce(ctx, record, func(_ *kgo.Record, err error) {
		if err != nil {
			log.Printf("[admin/api_keys] kafka produce failed: %v", err)
		}
	})
}

// _ keeps the messaging import explicit even if we later extract a shared helper
var _ = messaging.KafkaClient{}

// ExtractActorID pulls the admin's user_tracking_id out of the gin context
// (set by the JWT middleware). Falls back to "unknown" if the context value
// is missing — we still want a non-null actor in the audit table.
func ExtractActorID(actorFromCtx string) string {
	if actorFromCtx == "" {
		return "unknown"
	}
	return actorFromCtx
}

// ExtractClientIP is a tolerant client-IP extractor. Behind a load balancer
// the real IP lives in X-Forwarded-For; we take the first non-empty entry.
// If the env var OMNIGO_TRUST_PROXY=true is set we trust it; otherwise we
// use the connection's RemoteAddr to avoid header-injection spoofing.
func ExtractClientIP(xff, remoteAddr string, trustProxy bool) string {
	if trustProxy && xff != "" {
		parts := strings.Split(xff, ",")
		first := strings.TrimSpace(parts[0])
		if first != "" {
			return first
		}
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}
