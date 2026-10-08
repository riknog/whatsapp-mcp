package identity

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/riknog/whatsapp-mcp/internal/clock"
)

// RefPrefix starts every contact_ref. Resolver uses it to tell refs from names.
const RefPrefix = "c_"

// refLen is the number of base32 characters kept from the HMAC (50 bits).
const refLen = 10

// keySize is the size of the ref secret in bytes.
const keySize = 32

// keyMode is the only file mode accepted for ref.key.
const keyMode = 0o600

// readKeyAttempts and readKeyWait bound the wait for a key that another process
// is still writing (created, not yet filled).
const (
	readKeyAttempts = 20
	readKeyWait     = 5 * time.Millisecond
)

// KeyFileName is the name of the ref secret inside the data directory.
const KeyFileName = "ref.key"

// Refs turns WhatsApp JIDs into opaque contact refs. A ref is
// "c_" + base32lower(HMAC-SHA256(key, canonical JID))[:10]. It is stable for a
// given key and cannot be reversed without the key.
type Refs struct {
	key []byte
}

// NewRefs builds a Refs from an explicit key. The key must be 32 bytes.
func NewRefs(key []byte) (*Refs, error) {
	if len(key) != keySize {
		return nil, fmt.Errorf("identity: chave de ref com %d bytes, esperado %d", len(key), keySize)
	}
	k := make([]byte, keySize)
	copy(k, key)
	return &Refs{key: k}, nil
}

// LoadRefs is LoadRefsWithClock with the wall clock.
func LoadRefs(home string) (*Refs, error) {
	return LoadRefsWithClock(home, clock.Real{})
}

// LoadRefsWithClock reads <home>/ref.key. When the file does not exist, it
// creates it with 32 random bytes and mode 0600 (O_EXCL, so two processes cannot
// write different keys). An existing file must have mode 0600 and 32 bytes. An
// empty file is retried for a short while, because another process may be
// writing it right now. Otherwise it is an error.
func LoadRefsWithClock(home string, clk clock.Clock) (*Refs, error) {
	if clk == nil {
		clk = clock.Real{}
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, fmt.Errorf("identity: criar diretório de dados: %w", err)
	}
	path := filepath.Join(home, KeyFileName)

	key, err := createKey(path)
	if errors.Is(err, fs.ErrExist) {
		key, err = readKey(path, clk)
	}
	if err != nil {
		return nil, err
	}
	return NewRefs(key)
}

// createKey writes a new random key with O_EXCL. A fs.ErrExist result tells the
// caller to read the existing file instead.
func createKey(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- path is ref.key inside the data directory
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		return nil, fmt.Errorf("identity: criar ref.key: %w", err)
	}
	key := make([]byte, keySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("identity: gerar ref.key: %w", err)
	}
	if _, err := f.Write(key); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("identity: gravar ref.key: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("identity: fechar ref.key: %w", err)
	}
	return key, nil
}

func readKey(path string, clk clock.Clock) ([]byte, error) {
	for attempt := 1; ; attempt++ {
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("identity: ler ref.key: %w", err)
		}
		// Windows has no Unix modes: Perm() reports 0666 whatever the ACL,
		// and access is guarded by the profile directory's ACL instead.
		if mode := info.Mode().Perm(); mode != keyMode && runtime.GOOS != "windows" {
			return nil, fmt.Errorf("identity: ref.key com permissões %04o, esperado 0600; corrija com: chmod 600 %s", mode, path)
		}
		if info.Size() == 0 && attempt < readKeyAttempts {
			if err := clk.Sleep(context.Background(), readKeyWait); err != nil {
				return nil, fmt.Errorf("identity: esperar ref.key: %w", err)
			}
			continue
		}
		if info.Size() != keySize {
			return nil, fmt.Errorf("identity: ref.key com %d bytes, esperado %d", info.Size(), keySize)
		}
		key, err := os.ReadFile(path) // #nosec G304 -- path is ref.key inside the data directory
		if err != nil {
			return nil, fmt.Errorf("identity: ler ref.key: %w", err)
		}
		if len(key) != keySize {
			return nil, fmt.Errorf("identity: ref.key com %d bytes, esperado %d", len(key), keySize)
		}
		return key, nil
	}
}

// Ref returns the contact_ref of a JID. The JID is canonicalized first (lower
// case, device suffix removed), so "55...:12@s.whatsapp.net" and
// "55...@s.whatsapp.net" give the same ref.
func (r *Refs) Ref(jid string) string {
	mac := hmac.New(sha256.New, r.key)
	mac.Write([]byte(canonicalJID(jid)))
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(mac.Sum(nil))
	return RefPrefix + strings.ToLower(enc)[:refLen]
}

// canonicalJID lower-cases the JID and drops the ":device" suffix of the user part.
func canonicalJID(jid string) string {
	jid = strings.ToLower(strings.TrimSpace(jid))
	at := strings.IndexByte(jid, '@')
	if at < 0 {
		return jid
	}
	user, server := jid[:at], jid[at:]
	if colon := strings.IndexByte(user, ':'); colon >= 0 {
		user = user[:colon]
	}
	return user + server
}
