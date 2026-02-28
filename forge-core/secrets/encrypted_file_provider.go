package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"golang.org/x/crypto/argon2"
)

const (
	saltLen      = 16
	nonceLen     = 12
	argonTime    = 1
	argonMemory  = 64 * 1024 // 64 MB
	argonThreads = 4
	argonKeyLen  = 32
)

// EncryptedFileProvider stores secrets in an AES-256-GCM encrypted JSON file
// with Argon2id key derivation.
//
// File format: salt(16) || nonce(12) || AES-GCM-ciphertext
// Plaintext is JSON: {"key": "value", ...}
type EncryptedFileProvider struct {
	path       string
	passphrase func() (string, error) // callback to obtain the passphrase

	mu     sync.Mutex
	cache  map[string]string // in-memory cache after first decrypt
	loaded bool
}

// NewEncryptedFileProvider creates a provider that reads/writes an encrypted
// secrets file at path. The passphrase callback is invoked lazily on first
// access, keeping the core package free of terminal I/O.
func NewEncryptedFileProvider(path string, passphrase func() (string, error)) *EncryptedFileProvider {
	return &EncryptedFileProvider{
		path:       path,
		passphrase: passphrase,
	}
}

func (p *EncryptedFileProvider) Name() string { return "encrypted-file" }

// Get returns the secret for key, decrypting the file on first access.
func (p *EncryptedFileProvider) Get(key string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.ensureLoaded(); err != nil {
		return "", err
	}

	v, ok := p.cache[key]
	if !ok {
		return "", &ErrSecretNotFound{Key: key, Provider: p.Name()}
	}
	return v, nil
}

// List returns all secret keys in the encrypted file.
func (p *EncryptedFileProvider) List() ([]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.ensureLoaded(); err != nil {
		return nil, err
	}

	keys := make([]string, 0, len(p.cache))
	for k := range p.cache {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, nil
}

// Set stores or updates a secret and re-encrypts the file.
func (p *EncryptedFileProvider) Set(key, value string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.ensureLoaded(); err != nil {
		return err
	}

	p.cache[key] = value
	return p.flush()
}

// SetBatch stores or updates multiple secrets and re-encrypts the file once.
// This avoids repeated Argon2id key derivation when writing many secrets at once.
func (p *EncryptedFileProvider) SetBatch(pairs map[string]string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.ensureLoaded(); err != nil {
		return err
	}

	maps.Copy(p.cache, pairs)
	return p.flush()
}

// Delete removes a secret and re-encrypts the file.
func (p *EncryptedFileProvider) Delete(key string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.ensureLoaded(); err != nil {
		return err
	}

	if _, ok := p.cache[key]; !ok {
		return &ErrSecretNotFound{Key: key, Provider: p.Name()}
	}
	delete(p.cache, key)
	return p.flush()
}

// ensureLoaded decrypts the file into cache on first call.
// Caller must hold p.mu.
func (p *EncryptedFileProvider) ensureLoaded() error {
	if p.loaded {
		return nil
	}

	data, err := os.ReadFile(p.path)
	if os.IsNotExist(err) {
		// No file yet — start with an empty cache.
		p.cache = make(map[string]string)
		p.loaded = true
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading secrets file: %w", err)
	}

	pass, err := p.passphrase()
	if err != nil {
		return fmt.Errorf("obtaining passphrase: %w", err)
	}

	plaintext, err := decrypt(data, pass)
	if err != nil {
		return fmt.Errorf("decrypting secrets file: %w", err)
	}

	m := make(map[string]string)
	if err := json.Unmarshal(plaintext, &m); err != nil {
		return fmt.Errorf("parsing secrets: %w", err)
	}

	p.cache = m
	p.loaded = true
	return nil
}

// flush encrypts the cache and writes it atomically.
// Caller must hold p.mu.
func (p *EncryptedFileProvider) flush() error {
	pass, err := p.passphrase()
	if err != nil {
		return fmt.Errorf("obtaining passphrase: %w", err)
	}

	plaintext, err := json.Marshal(p.cache)
	if err != nil {
		return fmt.Errorf("marshalling secrets: %w", err)
	}

	ciphertext, err := encrypt(plaintext, pass)
	if err != nil {
		return err
	}

	// Atomic write: temp file → fsync → rename
	dir := filepath.Dir(p.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating secrets directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".secrets-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(ciphertext); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("syncing temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("closing temp file: %w", err)
	}
	if err := os.Chmod(tmpPath, 0600); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("setting permissions: %w", err)
	}
	if err := os.Rename(tmpPath, p.path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("renaming temp file: %w", err)
	}

	return nil
}

// deriveKey uses Argon2id to derive a 256-bit key from a passphrase and salt.
func deriveKey(passphrase string, salt []byte) []byte {
	return argon2.IDKey([]byte(passphrase), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
}

// encrypt produces: salt(16) || nonce(12) || AES-GCM-ciphertext
func encrypt(plaintext []byte, passphrase string) ([]byte, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("generating salt: %w", err)
	}

	key := deriveKey(passphrase, salt)

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("creating cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("creating GCM: %w", err)
	}

	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generating nonce: %w", err)
	}

	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)

	// salt || nonce || ciphertext
	result := make([]byte, 0, saltLen+nonceLen+len(ciphertext))
	result = append(result, salt...)
	result = append(result, nonce...)
	result = append(result, ciphertext...)
	return result, nil
}

// decrypt parses: salt(16) || nonce(12) || AES-GCM-ciphertext
func decrypt(data []byte, passphrase string) ([]byte, error) {
	minLen := saltLen + nonceLen + 1 // at least 1 byte of ciphertext
	if len(data) < minLen {
		return nil, fmt.Errorf("encrypted data too short: %d bytes", len(data))
	}

	salt := data[:saltLen]
	nonce := data[saltLen : saltLen+nonceLen]
	ciphertext := data[saltLen+nonceLen:]

	key := deriveKey(passphrase, salt)

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("creating cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("creating GCM: %w", err)
	}

	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decryption failed (wrong passphrase?): %w", err)
	}

	return plaintext, nil
}
