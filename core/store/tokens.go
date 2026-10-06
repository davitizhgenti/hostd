package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/davitizhgenti/hostd/internal/ids"
	"github.com/davitizhgenti/hostd/sdk"
)

// TokenPrefix starts every token secret, so leaked tokens are easy to
// recognize (for example by secret scanners).
const TokenPrefix = "hostd_"

var (
	// ErrInvalidToken: unknown, revoked or expired. Callers do not learn
	// which, so a guessed token reveals nothing.
	ErrInvalidToken = errors.New("invalid token")
	ErrNotFound     = errors.New("not found")
	ErrNameTaken    = errors.New("a token with this name already exists")
)

// Token is a device's or script's API credential. The secret itself is
// never stored, only its SHA-256 hash.
type Token struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Kind is the source kind of actions sent with this token: local (the
	// on-screen switcher), manual (a person's device) or automation
	// (scripts).
	Kind     sdk.SourceKind `json:"kind"`
	Scopes   []string       `json:"scopes"`
	Created  time.Time      `json:"created"`
	Expires  *time.Time     `json:"expires,omitempty"`
	Revoked  *time.Time     `json:"revoked,omitempty"`
	LastUsed *time.Time     `json:"last_used,omitempty"`
}

// HasScope reports whether the token grants scope; admin grants all.
func (t Token) HasScope(scope string) bool {
	for _, s := range t.Scopes {
		if s == scope || s == sdk.ScopeAdmin {
			return true
		}
	}
	return false
}

// ScopesWithin reports whether every scope in want is granted by have,
// for minting a token no stronger than its creator.
func ScopesWithin(want, have []string) bool {
	t := Token{Scopes: have}
	for _, s := range want {
		if !t.HasScope(s) {
			return false
		}
	}
	return true
}

func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// CreateToken creates a token and returns it with its secret, which is
// shown only now. ttl 0 means it never expires.
func (s *Store) CreateToken(ctx context.Context, name string, kind sdk.SourceKind, scopes []string, ttl time.Duration) (Token, string, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return Token{}, "", fmt.Errorf("token name is required")
	case kind != sdk.SourceLocal && kind != sdk.SourceManual && kind != sdk.SourceAutomation:
		return Token{}, "", fmt.Errorf("token kind must be local, manual or automation, not %q", kind)
	case len(scopes) == 0:
		return Token{}, "", fmt.Errorf("a token needs at least one scope")
	case ttl < 0:
		return Token{}, "", fmt.Errorf("negative lifetime")
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return Token{}, "", err
	}
	secret := TokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	now := s.clock.Now().UTC()
	t := Token{ID: ids.New(ids.Token), Name: name, Kind: kind, Scopes: scopes, Created: now}
	var expires sql.NullInt64
	if ttl > 0 {
		e := now.Add(ttl)
		t.Expires = &e
		expires = sql.NullInt64{Int64: unixNano(e), Valid: true}
	}
	scopesJSON, _ := json.Marshal(scopes)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO tokens (id, name, hash, kind, scopes, created, expires) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Name, hashSecret(secret), string(kind), string(scopesJSON), unixNano(now), expires)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: tokens.name") {
			return Token{}, "", fmt.Errorf("%w: %q", ErrNameTaken, name)
		}
		return Token{}, "", err
	}
	return t, secret, nil
}

// VerifyToken returns the token for a secret, recording its use. Unknown,
// revoked and expired tokens all give ErrInvalidToken.
func (s *Store) VerifyToken(ctx context.Context, secret string) (Token, error) {
	if !strings.HasPrefix(secret, TokenPrefix) {
		return Token{}, ErrInvalidToken
	}
	t, err := s.scanToken(s.db.QueryRowContext(ctx, tokenSelect+` WHERE hash = ?`, hashSecret(secret)))
	if errors.Is(err, sql.ErrNoRows) {
		return Token{}, ErrInvalidToken
	}
	if err != nil {
		return Token{}, err
	}
	now := s.clock.Now()
	if t.Revoked != nil || (t.Expires != nil && !now.Before(*t.Expires)) {
		return Token{}, ErrInvalidToken
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE tokens SET last_used = ? WHERE id = ?`, unixNano(now), t.ID); err != nil {
		return Token{}, err
	}
	u := now.UTC()
	t.LastUsed = &u
	return t, nil
}

// ListTokens returns all tokens, active and revoked, oldest first.
func (s *Store) ListTokens(ctx context.Context) ([]Token, error) {
	rows, err := s.db.QueryContext(ctx, tokenSelect+` ORDER BY created, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Token
	for rows.Next() {
		t, err := s.scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeToken revokes a token by ID. Its name becomes free for reuse.
func (s *Store) RevokeToken(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE tokens SET revoked = ? WHERE id = ? AND revoked IS NULL`,
		unixNano(s.clock.Now()), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// EnsureAdminToken creates the first admin token when the database has no
// tokens at all, returning its secret and created=true. Afterwards it does
// nothing, so a restart never mints a second admin token.
func (s *Store) EnsureAdminToken(ctx context.Context) (secret string, created bool, err error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM tokens`).Scan(&n); err != nil {
		return "", false, err
	}
	if n > 0 {
		return "", false, nil
	}
	_, secret, err = s.CreateToken(ctx, "admin", sdk.SourceManual, []string{sdk.ScopeAdmin}, 0)
	if err != nil {
		return "", false, err
	}
	return secret, true, nil
}

// PurgeExpired deletes tokens that expired or were revoked more than keep
// ago, so short-lived script tokens do not pile up.
func (s *Store) PurgeExpired(ctx context.Context, keep time.Duration) (int64, error) {
	cutoff := unixNano(s.clock.Now().Add(-keep))
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM tokens WHERE (expires IS NOT NULL AND expires < ?) OR (revoked IS NOT NULL AND revoked < ?)`,
		cutoff, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

const tokenSelect = `SELECT id, name, kind, scopes, created, expires, revoked, last_used FROM tokens`

type scanner interface{ Scan(dest ...any) error }

func (s *Store) scanToken(row scanner) (Token, error) {
	var (
		t                         Token
		kind, scopes              string
		created                   int64
		expires, revoked, lastUse sql.NullInt64
	)
	if err := row.Scan(&t.ID, &t.Name, &kind, &scopes, &created, &expires, &revoked, &lastUse); err != nil {
		return Token{}, err
	}
	t.Kind = sdk.SourceKind(kind)
	if err := json.Unmarshal([]byte(scopes), &t.Scopes); err != nil {
		return Token{}, fmt.Errorf("token %s: bad scopes: %w", t.ID, err)
	}
	t.Created = time.Unix(0, created).UTC()
	t.Expires, t.Revoked, t.LastUsed = fromNano(expires), fromNano(revoked), fromNano(lastUse)
	return t, nil
}
