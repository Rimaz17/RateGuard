package client

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"strings"
)

// A key reads rg_<tag>_<lookup>_<secret>:
//
//   - rg marks a RateGuard key, so secret scanners and people can spot one.
//   - tag is up to three characters taken from the client's name (rg_cus_
//     for "Customer App"), so a key in a log is easy to attribute.
//   - lookup is 80 random bits. With the tag it forms the public prefix,
//     which is stored, indexed and safe to log.
//   - secret is 256 random bits, never stored, never logged.
//
// Lowercase base32 uses no underscores, so the four parts split cleanly.
const (
	keyScheme   = "rg"
	lookupBytes = 10 // 16 base32 characters
	secretBytes = 32 // 52 base32 characters
	lookupLen   = 16
	secretLen   = 52
	maxTagLen   = 3
	fallbackTag = "app"

	// maxKeyLen bounds what Parse will look at. A real key is at most
	// 3+1+3+1+16+1+52 = 77 characters.
	maxKeyLen = 77
)

const base32Alphabet = "abcdefghijklmnopqrstuvwxyz234567"

// keyEncoding is immutable once built, so sharing it carries no state.
var keyEncoding = base32.NewEncoding(base32Alphabet).WithPadding(base32.NoPadding)

// generateKey returns a new full key and its public prefix. crypto/rand
// never fails on supported platforms (it crashes the program instead,
// documented since Go 1.24), so there is no error to return.
func generateKey(clientName string) (plaintext, prefix string) {
	var lookup [lookupBytes]byte
	var secret [secretBytes]byte
	_, _ = rand.Read(lookup[:])
	_, _ = rand.Read(secret[:])

	prefix = keyScheme + "_" + tagFor(clientName) + "_" + keyEncoding.EncodeToString(lookup[:])
	return prefix + "_" + keyEncoding.EncodeToString(secret[:]), prefix
}

// tagFor derives the readable tag from a client name: its first three
// ASCII letters or digits, lowercased. A name with none gets "app".
func tagFor(name string) string {
	// Byte-wise is safe: every byte of a multibyte UTF-8 character is at
	// least 0x80, so none can pass isTagChar.
	lower := strings.ToLower(name)
	var b strings.Builder
	for i := 0; i < len(lower) && b.Len() < maxTagLen; i++ {
		if isTagChar(lower[i]) {
			b.WriteByte(lower[i])
		}
	}
	if b.Len() == 0 {
		return fallbackTag
	}
	return b.String()
}

// parseKey returns the public prefix of a well-formed key. It checks shape
// only; whether the key is real is for the stored digest to say.
func parseKey(key string) (prefix string, ok bool) {
	if len(key) > maxKeyLen {
		return "", false
	}
	parts := strings.Split(key, "_")
	if len(parts) != 4 || parts[0] != keyScheme {
		return "", false
	}
	tag, lookup, secret := parts[1], parts[2], parts[3]
	if !validTag(tag) || len(lookup) != lookupLen || len(secret) != secretLen || !isBase32(lookup) || !isBase32(secret) {
		return "", false
	}
	return keyScheme + "_" + tag + "_" + lookup, true
}

func validTag(tag string) bool {
	if tag == "" || len(tag) > maxTagLen {
		return false
	}
	for i := range len(tag) {
		if !isTagChar(tag[i]) {
			return false
		}
	}
	return true
}

func isTagChar(c byte) bool {
	return ('a' <= c && c <= 'z') || ('0' <= c && c <= '9')
}

func isBase32(s string) bool {
	for i := range len(s) {
		if !strings.ContainsRune(base32Alphabet, rune(s[i])) {
			return false
		}
	}
	return true
}

// hashKey returns the SHA-256 digest that is stored in place of the key.
// Keys carry 256 bits of entropy, so a slow password hash would cost
// latency and buy nothing (ADR 0006).
func hashKey(key string) []byte {
	sum := sha256.Sum256([]byte(key))
	return sum[:]
}

// Redact returns the part of key that is safe to log: its public prefix,
// or a placeholder if it is not shaped like a key. Use it on any value
// that might be an API key before it goes near a log line or an error.
func Redact(key string) string {
	if prefix, ok := parseKey(key); ok {
		return prefix
	}
	return "[unparseable key]"
}
