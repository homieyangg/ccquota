// Package secret 提供 AES-256-GCM 對稱加解密,用於把 bot token 等敏感值加密存進 DB。
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
)

const encPrefix = "enc:"

// Cipher 以 AES-256-GCM 加解密字串。
type Cipher struct {
	aead cipher.AEAD
}

// New 以 32-byte 金鑰建立 Cipher。
func New(key []byte) (*Cipher, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("secret: key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead}, nil
}

// Encrypt 回傳 "enc:" + base64(nonce || ciphertext)。
func (c *Cipher) Encrypt(plaintext string) (string, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := c.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return encPrefix + base64.StdEncoding.EncodeToString(ct), nil
}

// Decrypt 解開 Encrypt 產生的字串;沒有 "enc:" 前綴時視為明文原樣回傳(向後相容)。
func (c *Cipher) Decrypt(s string) (string, error) {
	if !strings.HasPrefix(s, encPrefix) {
		return s, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, encPrefix))
	if err != nil {
		return "", err
	}
	ns := c.aead.NonceSize()
	if len(raw) < ns {
		return "", fmt.Errorf("secret: ciphertext too short")
	}
	nonce, ct := raw[:ns], raw[ns:]
	pt, err := c.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

// LoadKey 解析金鑰:env(base64 32 bytes)優先;否則讀 keyfile;
// keyfile 不存在則隨機產生並以 0600 寫入。
func LoadKey(envVal, keyfilePath string) ([]byte, error) {
	if envVal != "" {
		k, err := base64.StdEncoding.DecodeString(envVal)
		if err != nil {
			return nil, fmt.Errorf("secret: bad CCQUOTA_SECRET_KEY base64: %w", err)
		}
		if len(k) != 32 {
			return nil, fmt.Errorf("secret: CCQUOTA_SECRET_KEY must decode to 32 bytes, got %d", len(k))
		}
		return k, nil
	}
	if b, err := os.ReadFile(keyfilePath); err == nil {
		if k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b))); err == nil && len(k) == 32 {
			return k, nil
		}
	}
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	enc := base64.StdEncoding.EncodeToString(k)
	if err := os.WriteFile(keyfilePath, []byte(enc), 0600); err != nil {
		return nil, fmt.Errorf("secret: write keyfile: %w", err)
	}
	return k, nil
}
