package platform

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"sync"
)

// Since Windmill 1.855.0 a flow step's input transform that computes a
// value from a secret flow_env entry stores it as an $encrypted: value
// under a key of the flow's root job, which the receiving script job
// decrypts, and so do a subflow's steps, which share that root (JE-38,
// JE-42). windmill-lite holds no workspace key, so it seals such a value
// with a key of its own: one random key per process, from which each root
// job's key is derived. Only this process opens the value, and only for
// the same root; a value Windmill encrypted, or one sealed for another
// root, is not windmill-lite's to open and goes to the host's secret
// service as before (Secrets.Decrypt).

// sealMagic starts every value windmill-lite seals, before base64, so
// that opening one tells it from any other $encrypted: value at once.
const sealMagic = "wl\x00\x01"

// processKey is the process's random key, made when first needed.
var processKey = sync.OnceValue(func() []byte {
	k := make([]byte, 32)
	rand.Read(k) // never fails, or crashes the program (crypto/rand)
	return k
})

// rootAEAD is the AES-256-GCM cipher of the root job root, with a random
// nonce that each sealed value carries.
func rootAEAD(root string) (cipher.AEAD, error) {
	key, err := hkdf.Key(sha256.New, processKey(), nil, "windmill-lite $encrypted: "+root, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithRandomNonce(block)
}

// SealForRoot returns plaintext as an $encrypted: value under the key of
// the root job root (JE-38): "$encrypted:" and standard padded base64,
// different every time. ResolveArgs gives the plaintext back to a job of
// the same root (its WM_ROOT_JOB_ID), parsed as JSON.
func SealForRoot(root string, plaintext []byte) (string, error) {
	aead, err := rootAEAD(root)
	if err != nil {
		return "", err
	}
	sealed := aead.Seal([]byte(sealMagic), nil, plaintext, nil)
	return "$encrypted:" + base64.StdEncoding.EncodeToString(sealed), nil
}

// openForRoot returns the plaintext of blob, the part of an $encrypted:
// value after the marker, and true when windmill-lite sealed it for the
// root job root in this process; false otherwise.
func openForRoot(root, blob string) (string, bool) {
	if root == "" {
		return "", false
	}
	b, err := base64.StdEncoding.Strict().DecodeString(blob)
	if err != nil || !bytes.HasPrefix(b, []byte(sealMagic)) {
		return "", false
	}
	aead, err := rootAEAD(root)
	if err != nil {
		return "", false
	}
	plain, err := aead.Open(nil, nil, b[len(sealMagic):], nil)
	if err != nil {
		return "", false
	}
	return string(plain), true
}

// SecretVariable reports whether the variable at path is stored as a
// secret, and false when there is none: whether a flow_env entry that
// references it is secret (JE-38).
func (s *Services) SecretVariable(ctx context.Context, path string) (bool, error) {
	v, ok, err := s.variable(ctx, path)
	if err != nil {
		return false, err
	}
	return ok && v.IsSecret, nil
}

// SecretEntry reports whether a flow_env entry, its stored JSON text, is
// secret (JE-38): a literal $encrypted: string, or a $var: or $jsonvar:
// reference to a variable stored as secret. A $res: reference, any other
// string and any other value are not.
func (s *Services) SecretEntry(ctx context.Context, entry []byte) (bool, error) {
	n, err := parseJSON(entry)
	if err != nil || n.kind != kString {
		return false, nil
	}
	switch str := n.text; {
	case strings.HasPrefix(str, "$encrypted:"):
		return true, nil
	case strings.HasPrefix(str, "$var:"):
		return s.SecretVariable(ctx, str[len("$var:"):])
	case strings.HasPrefix(str, "$jsonvar:"):
		return s.SecretVariable(ctx, str[len("$jsonvar:"):])
	}
	return false, nil
}
