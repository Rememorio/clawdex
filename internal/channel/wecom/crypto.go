// Package wecom implements WeCom (企业微信) channel driver for the gateway.
package wecom

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// verifySignature checks the SHA1 signature from WeCom callback parameters.
// sig = SHA1(sort([token, timestamp, nonce, encrypted]))
func verifySignature(token, timestamp, nonce, encrypted, msgSignature string) bool {
	parts := []string{token, timestamp, nonce, encrypted}
	sort.Strings(parts)
	h := sha1.New()
	h.Write([]byte(strings.Join(parts, "")))
	computed := fmt.Sprintf("%x", h.Sum(nil))
	return computed == msgSignature
}

// decrypt decodes and decrypts an AES-256-CBC encrypted WeCom message.
// The encodingAESKey is a 43-character base64 string (without trailing "=").
// Returns the decrypted XML content.
func decrypt(encodingAESKey, encrypted string) (string, error) {
	// Derive 32-byte AES key from the 43-char EncodingAESKey.
	aesKey, err := base64.StdEncoding.DecodeString(encodingAESKey + "=")
	if err != nil {
		return "", fmt.Errorf("decode encoding_aes_key: %w", err)
	}
	if len(aesKey) != 32 {
		return "", fmt.Errorf("invalid aes key length: got %d, want 32", len(aesKey))
	}

	// IV is the first 16 bytes of the key.
	iv := aesKey[:16]

	ciphertext, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil {
		return "", fmt.Errorf("decode encrypted content: %w", err)
	}

	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return "", fmt.Errorf("create aes cipher: %w", err)
	}
	if len(ciphertext)%aes.BlockSize != 0 {
		return "", fmt.Errorf("ciphertext length %d is not a multiple of block size %d", len(ciphertext), aes.BlockSize)
	}

	mode := cipher.NewCBCDecrypter(block, iv)
	plaintext := make([]byte, len(ciphertext))
	mode.CryptBlocks(plaintext, ciphertext)

	// PKCS7 unpad.
	// Note: WeCom may produce padding values larger than AES block size (16)
	// when the plaintext is short. The pad value can be up to len(plaintext).
	if len(plaintext) == 0 {
		return "", fmt.Errorf("decrypted content is empty")
	}
	padLen := int(plaintext[len(plaintext)-1])
	if padLen < 1 || padLen > len(plaintext) {
		return "", fmt.Errorf("invalid PKCS7 padding: %d", padLen)
	}
	plaintext = plaintext[:len(plaintext)-padLen]

	// Format: 16 random bytes + 4-byte msg length (big-endian) + msg content + receiveid
	if len(plaintext) < 20 {
		return "", fmt.Errorf("decrypted content too short: %d bytes", len(plaintext))
	}
	msgLen := binary.BigEndian.Uint32(plaintext[16:20])
	if 20+int(msgLen) > len(plaintext) {
		return "", fmt.Errorf("message length %d exceeds available data %d", msgLen, len(plaintext)-20)
	}
	return string(plaintext[20 : 20+msgLen]), nil
}

// decryptFileData decrypts AES-256-CBC encrypted file data from WeCom.
// Unlike message decrypt(), file encryption has no 16-byte random + 4-byte msgLen wrapper.
// Key = base64_decode(encodingAESKey + "="), IV = key[:16], PKCS#7 unpad (1-32).
func decryptFileData(encodingAESKey string, data []byte) ([]byte, error) {
	aesKey, err := base64.StdEncoding.DecodeString(encodingAESKey + "=")
	if err != nil {
		return nil, fmt.Errorf("decode encoding_aes_key: %w", err)
	}
	if len(aesKey) != 32 {
		return nil, fmt.Errorf("invalid aes key length: got %d, want 32", len(aesKey))
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("empty encrypted data")
	}
	if len(data)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("data length %d is not a multiple of block size %d", len(data), aes.BlockSize)
	}

	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return nil, fmt.Errorf("create aes cipher: %w", err)
	}

	iv := aesKey[:aes.BlockSize]
	plaintext := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plaintext, data)

	// PKCS#7 unpad — WeCom uses pad values 1-32 (AES-256 key size).
	if len(plaintext) == 0 {
		return nil, fmt.Errorf("decrypted content is empty")
	}
	padLen := int(plaintext[len(plaintext)-1])
	if padLen < 1 || padLen > 32 {
		return nil, fmt.Errorf("invalid PKCS7 padding: %d", padLen)
	}
	if padLen > len(plaintext) {
		return nil, fmt.Errorf("padding %d exceeds data length %d", padLen, len(plaintext))
	}
	return plaintext[:len(plaintext)-padLen], nil
}

// extractAPIKey parses the "key" query parameter from a WeCom webhook URL.
func extractAPIKey(webhookURL string) (string, error) {
	u, err := url.Parse(webhookURL)
	if err != nil {
		return "", fmt.Errorf("parse webhook url: %w", err)
	}
	key := u.Query().Get("key")
	if key == "" {
		return "", fmt.Errorf("missing key parameter in webhook url")
	}
	return key, nil
}
