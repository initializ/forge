package trust

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
)

// GenerateKeyPair creates a new Ed25519 key pair.
func GenerateKeyPair() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generating key pair: %w", err)
	}
	return pub, priv, nil
}

// Sign produces an Ed25519 signature of content.
func Sign(content []byte, privateKey ed25519.PrivateKey) []byte {
	return ed25519.Sign(privateKey, content)
}

// Verify checks an Ed25519 signature of content.
func Verify(content []byte, signature []byte, publicKey ed25519.PublicKey) bool {
	return ed25519.Verify(publicKey, content, signature)
}

// SignSkill produces a detached signature for skill content.
func SignSkill(skillContent []byte, privateKey ed25519.PrivateKey) ([]byte, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("invalid private key size: %d", len(privateKey))
	}
	return Sign(skillContent, privateKey), nil
}

// VerifySkill checks a detached signature for skill content.
func VerifySkill(skillContent, signature []byte, publicKey ed25519.PublicKey) bool {
	if len(publicKey) != ed25519.PublicKeySize || len(signature) != ed25519.SignatureSize {
		return false
	}
	return Verify(skillContent, signature, publicKey)
}
