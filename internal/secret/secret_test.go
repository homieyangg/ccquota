package secret

import "testing"

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	c, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := c.Encrypt("hello-token")
	if err != nil {
		t.Fatal(err)
	}
	if enc == "hello-token" {
		t.Fatal("ciphertext equals plaintext")
	}
	if got, _ := c.Decrypt(enc); got != "hello-token" {
		t.Fatalf("round-trip got %q", got)
	}
}

func TestDecryptPlaintextFallback(t *testing.T) {
	key := make([]byte, 32)
	c, _ := New(key)
	// 沒有 enc: 前綴 → 視為明文原樣回傳(向後相容)
	if got, _ := c.Decrypt("legacy-plain"); got != "legacy-plain" {
		t.Fatalf("got %q", got)
	}
}

func TestNewRejectsBadKeyLen(t *testing.T) {
	if _, err := New(make([]byte, 16)); err == nil {
		t.Fatal("want error for 16-byte key")
	}
}

func TestDecryptWrongKeyFails(t *testing.T) {
	k1 := make([]byte, 32)
	k2 := make([]byte, 32)
	k2[0] = 1
	c1, _ := New(k1)
	c2, _ := New(k2)
	enc, _ := c1.Encrypt("x")
	if _, err := c2.Decrypt(enc); err == nil {
		t.Fatal("want error decrypting with wrong key")
	}
}
