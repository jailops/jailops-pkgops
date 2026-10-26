package repository

import (
	"jailops.net/pkgops/internal/pkgerr"

	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
)

// SignedEntry is one extra file stored in a catalog archive next to the
// payload (the signature, and in fingerprint mode the public key).
type SignedEntry struct {
	Name string
	Data []byte
}

// Signer signs repository catalogs. payload is the name of the file inside the
// archive ("data", "packagesite.yaml" or "files") and digestHex the lowercase
// hex SHA-256 of that file's content.
type Signer interface {
	SignCatalog(payload, digestHex string) ([]SignedEntry, error)
}

// SignMode selects how clients are expected to trust the key.
type SignMode int

const (
	// SignPubkey stores a single "signature" entry. Clients configure
	// signature_type: "pubkey" and pubkey: "/path/repo.pub".
	SignPubkey SignMode = iota
	// SignFingerprints stores "<payload>.sig" and "<payload>.pub". Clients
	// configure signature_type: "fingerprints" and a fingerprints directory
	// holding the output of [FingerprintFile] under trusted/.
	SignFingerprints
)

// RSASigner signs with an RSA key using PKCS#1 v1.5 / SHA-256 over the hex
// digest string, the convention of pkg-repo(8).
type RSASigner struct {
	Key  *rsa.PrivateKey
	Mode SignMode
}

// SignCatalog implements [Signer].
func (s *RSASigner) SignCatalog(payload, digestHex string) ([]SignedEntry, error) {
	if s.Key == nil {
		return nil, fmt.Errorf("%w: no key", pkgerr.ErrSignature)
	}
	h := sha256.Sum256([]byte(digestHex))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.Key, crypto.SHA256, h[:])
	if err != nil {
		return nil, fmt.Errorf("%w: %v", pkgerr.ErrSignature, err)
	}
	switch s.Mode {
	case SignPubkey:
		// pkg-repo(8) stores the signature followed by a NUL byte.
		return []SignedEntry{{Name: "signature", Data: append(sig, 0)}}, nil
	case SignFingerprints:
		pub, err := PublicKeyPEM(&s.Key.PublicKey)
		if err != nil {
			return nil, err
		}
		return []SignedEntry{{Name: payload + ".sig", Data: sig}, {Name: payload + ".pub", Data: pub}}, nil
	}
	return nil, fmt.Errorf("%w: unknown sign mode", pkgerr.ErrSignature)
}

// GenerateRSAKey creates a new RSA key (bits >= 2048).
func GenerateRSAKey(bits int) (*rsa.PrivateKey, error) {
	if bits < 2048 {
		return nil, fmt.Errorf("%w: key must be at least 2048 bits", pkgerr.ErrSignature)
	}
	return rsa.GenerateKey(rand.Reader, bits)
}

// PrivateKeyPEM encodes a key as PKCS#8 PEM (what current OpenSSL writes).
func PrivateKeyPEM(k *rsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// PublicKeyPEM encodes the public key as PKIX PEM, byte-identical to
// `openssl rsa -pubout`.
func PublicKeyPEM(k *rsa.PublicKey) ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(k)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil
}

// ParseRSAPrivateKey parses a PKCS#1 or PKCS#8 PEM private key.
func ParseRSAPrivateKey(data []byte) (*rsa.PrivateKey, error) {
	b, _ := pem.Decode(data)
	if b == nil {
		return nil, fmt.Errorf("%w: no PEM data in key", pkgerr.ErrSignature)
	}
	if k, err := x509.ParsePKCS1PrivateKey(b.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%w: unreadable private key: %v", pkgerr.ErrSignature, err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%w: only RSA keys are supported", pkgerr.ErrSignature)
	}
	return rk, nil
}

// LoadRSASigner reads a PEM private key file.
func LoadRSASigner(file string, mode SignMode) (*RSASigner, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	k, err := ParseRSAPrivateKey(data)
	if err != nil {
		return nil, err
	}
	return &RSASigner{Key: k, Mode: mode}, nil
}

// ParseRSAPublicKey parses a PKIX PEM public key.
func ParseRSAPublicKey(data []byte) (*rsa.PublicKey, error) {
	b, _ := pem.Decode(data)
	if b == nil {
		return nil, fmt.Errorf("%w: no PEM data in public key", pkgerr.ErrSignature)
	}
	k, err := x509.ParsePKIXPublicKey(b.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", pkgerr.ErrSignature, err)
	}
	rk, ok := k.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("%w: only RSA keys are supported", pkgerr.ErrSignature)
	}
	return rk, nil
}

// VerifyRSA checks a signature made by [RSASigner] (trailing NUL tolerated).
func VerifyRSA(pub *rsa.PublicKey, digestHex string, sig []byte) error {
	if n := pub.Size(); len(sig) == n+1 && sig[n] == 0 {
		sig = sig[:n]
	}
	h := sha256.Sum256([]byte(digestHex))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, h[:], sig); err != nil {
		return fmt.Errorf("%w: %v", pkgerr.ErrSignature, errors.New("signature does not match"))
	}
	return nil
}

// Fingerprint returns the sha256 of the public key PEM exactly as distributed
// (the value pkg expects in a fingerprints file).
func Fingerprint(pubPEM []byte) string {
	h := sha256.Sum256(pubPEM)
	return hex.EncodeToString(h[:])
}

// FingerprintFile renders the file clients place in
// <fingerprints dir>/trusted/ for SignFingerprints repositories.
func FingerprintFile(pubPEM []byte) []byte {
	return []byte("function: sha256\nfingerprint: \"" + Fingerprint(pubPEM) + "\"\n")
}
