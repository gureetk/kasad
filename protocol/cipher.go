// Package protocol implements the local control protocol spoken by older
// TP-Link Kasa smart plugs on TCP port 9999.
//
// A request is a JSON document. On the wire it is obfuscated with an autokey
// XOR cipher and, over TCP, preceded by its length as a 4-byte big-endian
// integer. Replies use the same encoding.
//
// The protocol has no authentication: anything that can reach the port can
// control the device. The cipher hides the JSON from a casual glance and
// nothing more.
package protocol

// initialKey is the key the cipher starts with for every message.
const initialKey byte = 0xAB

// Encrypt returns the obfuscated form of plaintext.
//
// Each byte is XORed with the current key, and the resulting ciphertext byte
// becomes the key for the next one. The input is not modified.
func Encrypt(plaintext []byte) []byte {
	out := make([]byte, len(plaintext))
	encryptInto(out, plaintext)
	return out
}

// Decrypt reverses Encrypt. The input is not modified.
//
// Each byte is XORed with the current key, and that ciphertext byte, not the
// recovered plaintext, becomes the key for the next one.
func Decrypt(ciphertext []byte) []byte {
	out := make([]byte, len(ciphertext))
	copy(out, ciphertext)
	decryptInPlace(out)
	return out
}

// encryptInto writes the ciphertext of src to dst, which must be at least as
// long as src.
func encryptInto(dst, src []byte) {
	key := initialKey
	for i, b := range src {
		key ^= b
		dst[i] = key
	}
}

// decryptInPlace replaces the ciphertext in buf with its plaintext.
func decryptInPlace(buf []byte) {
	key := initialKey
	for i, b := range buf {
		buf[i] = key ^ b
		key = b
	}
}
