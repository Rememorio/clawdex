package weixin

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAESECBRoundTrip(t *testing.T) {
	key := []byte("0123456789abcdef") // 16 bytes
	plaintext := []byte("Hello, Weixin! This is a test message for AES-128-ECB.")

	ciphertext, err := aesECBEncrypt(plaintext, key)
	require.NoError(t, err)
	assert.NotEqual(t, plaintext, ciphertext)
	assert.Equal(t, 0, len(ciphertext)%16) // block-aligned

	decrypted, err := aesECBDecrypt(ciphertext, key)
	require.NoError(t, err)
	assert.Equal(t, plaintext, decrypted)
}

func TestAESECBEmptyPlaintext(t *testing.T) {
	key := []byte("0123456789abcdef")
	// Empty plaintext should produce one block of padding.
	ciphertext, err := aesECBEncrypt([]byte{}, key)
	require.NoError(t, err)
	assert.Equal(t, 16, len(ciphertext))

	decrypted, err := aesECBDecrypt(ciphertext, key)
	require.NoError(t, err)
	assert.Equal(t, []byte{}, decrypted)
}

func TestAESECBInvalidKeySize(t *testing.T) {
	_, err := aesECBEncrypt([]byte("test"), []byte("short"))
	assert.Error(t, err)

	_, err = aesECBDecrypt(make([]byte, 16), []byte("short"))
	assert.Error(t, err)
}

func TestAESECBDecryptInvalidLength(t *testing.T) {
	key := []byte("0123456789abcdef")
	// Not a multiple of 16.
	_, err := aesECBDecrypt([]byte("not-aligned-data"), key)
	assert.Error(t, err)
}

func TestAESECBDecryptEmptyCiphertext(t *testing.T) {
	key := []byte("0123456789abcdef")
	_, err := aesECBDecrypt([]byte{}, key)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not a multiple")
}

func TestPKCS7PadUnpad(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"one byte", []byte{0x42}},
		{"block aligned", make([]byte, 16)},
		{"multi-block", make([]byte, 33)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			padded := pkcs7Pad(tc.data, 16)
			assert.Equal(t, 0, len(padded)%16)
			unpadded, err := pkcs7Unpad(padded, 16)
			require.NoError(t, err)
			assert.Equal(t, tc.data, unpadded)
		})
	}
}

func TestPKCS7UnpadInvalidPadding(t *testing.T) {
	// Padding byte is 0.
	_, err := pkcs7Unpad([]byte{1, 2, 3, 0}, 4)
	assert.Error(t, err)

	// Padding byte > block size.
	_, err = pkcs7Unpad([]byte{1, 2, 3, 5}, 4)
	assert.Error(t, err)

	// Inconsistent padding bytes.
	_, err = pkcs7Unpad([]byte{1, 2, 2, 3}, 4) // last byte says 3 but byte at -3 is 2
	assert.Error(t, err)

	// Empty data.
	_, err = pkcs7Unpad([]byte{}, 4)
	assert.Error(t, err)
}

func TestGenerateAESKey(t *testing.T) {
	key, err := generateAESKey()
	require.NoError(t, err)
	assert.Equal(t, 16, len(key))

	// Should be random (different each time).
	key2, _ := generateAESKey()
	assert.NotEqual(t, key, key2)
}

func TestGenerateFileKey(t *testing.T) {
	fk, err := generateFileKey()
	require.NoError(t, err)
	assert.Equal(t, 32, len(fk)) // hex-encoded 16 bytes

	// Verify it's valid hex.
	_, err = hex.DecodeString(fk)
	assert.NoError(t, err)
}

func TestFileMD5(t *testing.T) {
	// Known MD5 of empty data.
	assert.Equal(t, "d41d8cd98f00b204e9800998ecf8427e", fileMD5([]byte{}))
	// Non-empty.
	md5 := fileMD5([]byte("hello"))
	assert.Equal(t, "5d41402abc4b2a76b9719d911017c592", md5)
}

func TestAESEncryptedSize(t *testing.T) {
	// 0 bytes → 16 (one full block of padding)
	assert.Equal(t, int64(16), aesEncryptedSize(0))
	// 1 byte → 16
	assert.Equal(t, int64(16), aesEncryptedSize(1))
	// 15 bytes → 16
	assert.Equal(t, int64(16), aesEncryptedSize(15))
	// 16 bytes → 32 (full padding block added)
	assert.Equal(t, int64(32), aesEncryptedSize(16))
	// 17 bytes → 32
	assert.Equal(t, int64(32), aesEncryptedSize(17))
}
