package weixin

import (
	"bytes"
	"crypto/aes"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
)

// aesECBEncrypt encrypts plaintext using AES-128-ECB with PKCS7 padding.
// keyBytes must be exactly 16 bytes.
func aesECBEncrypt(plaintext, keyBytes []byte) ([]byte, error) {
	block, err := aes.NewCipher(keyBytes)
	if err != nil {
		return nil, fmt.Errorf("aes cipher: %w", err)
	}
	blockSize := block.BlockSize()
	padded := pkcs7Pad(plaintext, blockSize)
	ciphertext := make([]byte, len(padded))
	for i := 0; i < len(padded); i += blockSize {
		block.Encrypt(ciphertext[i:i+blockSize], padded[i:i+blockSize])
	}
	return ciphertext, nil
}

// aesECBDecrypt decrypts AES-128-ECB ciphertext and removes PKCS7 padding.
// keyBytes must be exactly 16 bytes.
func aesECBDecrypt(ciphertext, keyBytes []byte) ([]byte, error) {
	block, err := aes.NewCipher(keyBytes)
	if err != nil {
		return nil, fmt.Errorf("aes cipher: %w", err)
	}
	blockSize := block.BlockSize()
	if len(ciphertext) == 0 || len(ciphertext)%blockSize != 0 {
		return nil, fmt.Errorf("ciphertext length %d is not a multiple of block size %d", len(ciphertext), blockSize)
	}
	plaintext := make([]byte, len(ciphertext))
	for i := 0; i < len(ciphertext); i += blockSize {
		block.Decrypt(plaintext[i:i+blockSize], ciphertext[i:i+blockSize])
	}
	return pkcs7Unpad(plaintext, blockSize)
}

// pkcs7Pad appends PKCS#7 padding to data.
func pkcs7Pad(data []byte, blockSize int) []byte {
	padding := blockSize - len(data)%blockSize
	pad := bytes.Repeat([]byte{byte(padding)}, padding)
	return append(data, pad...)
}

// pkcs7Unpad removes PKCS#7 padding.
func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty data")
	}
	padding := int(data[len(data)-1])
	if padding == 0 || padding > blockSize || padding > len(data) {
		return nil, fmt.Errorf("invalid pkcs7 padding: %d", padding)
	}
	for i := len(data) - padding; i < len(data); i++ {
		if data[i] != byte(padding) {
			return nil, fmt.Errorf("invalid pkcs7 padding byte at offset %d", i)
		}
	}
	return data[:len(data)-padding], nil
}

// generateAESKey generates a random 16-byte AES key.
func generateAESKey() ([]byte, error) {
	key := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("generate aes key: %w", err)
	}
	return key, nil
}

// generateFileKey generates a random 16-byte hex-encoded file key.
func generateFileKey() (string, error) {
	b := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", fmt.Errorf("generate file key: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// fileMD5 computes the hex-encoded MD5 of data.
func fileMD5(data []byte) string {
	h := md5.Sum(data)
	return hex.EncodeToString(h[:])
}

// aesEncryptedSize returns the AES-128-ECB ciphertext size for a given plaintext size.
func aesEncryptedSize(plaintextSize int64) int64 {
	blockSize := int64(aes.BlockSize)
	padding := blockSize - plaintextSize%blockSize
	return plaintextSize + padding
}
