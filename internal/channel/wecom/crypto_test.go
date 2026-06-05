package wecom

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerifySignature(t *testing.T) {
	token := "test_token"
	timestamp := "1234567890"
	nonce := "abc123"
	encrypted := "encrypted_content"

	// Compute expected signature manually.
	// sorted: 1234567890, abc123, encrypted_content, test_token
	h := sha1.New()
	h.Write([]byte("1234567890abc123encrypted_contenttest_token"))
	expected := fmt.Sprintf("%x", h.Sum(nil))

	assert.True(t, verifySignature(token, timestamp, nonce, encrypted, expected))
	assert.False(t, verifySignature(token, timestamp, nonce, encrypted, "wrong_signature"))
}

func TestDecrypt(t *testing.T) {
	// Build a test encrypted message with known content.
	msg := []byte("hello wecom")

	// Use a known 43-char base64 key (32 bytes when decoded with trailing "=").
	encodingAESKey := "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG"
	aesKey, err := base64.StdEncoding.DecodeString(encodingAESKey + "=")
	require.NoError(t, err)
	require.Len(t, aesKey, 32)

	iv := aesKey[:16]

	// Construct plaintext: 16 random + 4-byte length + message + receiveid
	random := make([]byte, 16)
	for i := range random {
		random[i] = byte(i)
	}
	msgLenBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(msgLenBuf, uint32(len(msg)))
	receiveid := []byte("corpid")
	plaintext := append(random, msgLenBuf...)
	plaintext = append(plaintext, msg...)
	plaintext = append(plaintext, receiveid...)

	// PKCS7 pad.
	padLen := aes.BlockSize - (len(plaintext) % aes.BlockSize)
	for i := 0; i < padLen; i++ {
		plaintext = append(plaintext, byte(padLen))
	}

	// Encrypt.
	block, err := aes.NewCipher(aesKey)
	require.NoError(t, err)
	ciphertext := make([]byte, len(plaintext))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ciphertext, plaintext)

	encrypted := base64.StdEncoding.EncodeToString(ciphertext)

	result, err := decrypt(encodingAESKey, encrypted)
	require.NoError(t, err)
	assert.Equal(t, "hello wecom", result)
}

func TestDecryptInvalidKey(t *testing.T) {
	_, err := decrypt("short", "dGVzdA==")
	assert.Error(t, err)
}

func TestExtractAPIKey(t *testing.T) {
	key, err := extractAPIKey("https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=abc123")
	require.NoError(t, err)
	assert.Equal(t, "abc123", key)

	_, err = extractAPIKey("https://qyapi.weixin.qq.com/cgi-bin/webhook/send")
	assert.Error(t, err)
}
