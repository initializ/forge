package trust

import (
	"crypto/ed25519"
	"testing"
)

func TestGenerateKeyPair(t *testing.T) {
	pub, priv, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair failed: %v", err)
	}
	if len(pub) != ed25519.PublicKeySize {
		t.Fatalf("public key wrong size: %d", len(pub))
	}
	if len(priv) != ed25519.PrivateKeySize {
		t.Fatalf("private key wrong size: %d", len(priv))
	}
}

func TestSignAndVerify(t *testing.T) {
	pub, priv, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}

	content := []byte("skill content to sign")
	sig := Sign(content, priv)

	if !Verify(content, sig, pub) {
		t.Fatal("signature verification failed")
	}

	// Tampered content should fail
	if Verify([]byte("tampered"), sig, pub) {
		t.Fatal("verification passed for tampered content")
	}
}

func TestSignSkillAndVerifySkill(t *testing.T) {
	pub, priv, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}

	content := []byte("# My Skill\nSome content here")
	sig, err := SignSkill(content, priv)
	if err != nil {
		t.Fatalf("SignSkill failed: %v", err)
	}

	if !VerifySkill(content, sig, pub) {
		t.Fatal("VerifySkill failed for valid signature")
	}

	// Wrong key should fail
	pub2, _, _ := GenerateKeyPair()
	if VerifySkill(content, sig, pub2) {
		t.Fatal("VerifySkill passed for wrong key")
	}
}

func TestSignSkill_InvalidKey(t *testing.T) {
	_, err := SignSkill([]byte("content"), []byte("short"))
	if err == nil {
		t.Fatal("expected error for invalid key")
	}
}

func TestVerifySkill_InvalidInputs(t *testing.T) {
	if VerifySkill([]byte("c"), []byte("short"), []byte("short")) {
		t.Fatal("should fail with invalid inputs")
	}
}
