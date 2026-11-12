package protocol

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// A known plaintext and its ciphertext, used as a fixed reference point for
// the cipher.
const (
	sysinfoRequest       = `{"system":{"get_sysinfo":{}}}`
	sysinfoCiphertextHex = "d0f281f88bff9af7d5ef94b6d1b4c09fec95e68fe187e8caf08bf68bf6"
)

func sysinfoCiphertext(t testing.TB) []byte {
	t.Helper()
	ciphertext, err := hex.DecodeString(sysinfoCiphertextHex)
	if err != nil {
		t.Fatalf("decode reference ciphertext: %v", err)
	}
	return ciphertext
}

func TestEncryptKnownVector(t *testing.T) {
	want := sysinfoCiphertext(t)
	got := Encrypt([]byte(sysinfoRequest))
	if !bytes.Equal(got, want) {
		t.Errorf("Encrypt(%q)\n got %x\nwant %x", sysinfoRequest, got, want)
	}
}

func TestDecryptKnownVector(t *testing.T) {
	got := Decrypt(sysinfoCiphertext(t))
	if string(got) != sysinfoRequest {
		t.Errorf("Decrypt(reference) = %q, want %q", got, sysinfoRequest)
	}
}

func TestCipherRoundTrip(t *testing.T) {
	allBytes := make([]byte, 256)
	for i := range allBytes {
		allBytes[i] = byte(i)
	}
	cases := map[string][]byte{
		"empty":          {},
		"single byte":    {0x7B},
		"initial key":    {initialKey},
		"zeros":          make([]byte, 16),
		"every byte":     allBytes,
		"relay request":  []byte(`{"system":{"set_relay_state":{"state":1}}}`),
		"non-ASCII text": []byte(`{"alias":"Garagentür – links"}`),
	}
	for name, plaintext := range cases {
		t.Run(name, func(t *testing.T) {
			ciphertext := Encrypt(plaintext)
			if len(ciphertext) != len(plaintext) {
				t.Fatalf("ciphertext is %d bytes, plaintext is %d", len(ciphertext), len(plaintext))
			}
			if got := Decrypt(ciphertext); !bytes.Equal(got, plaintext) {
				t.Errorf("round trip\n got %x\nwant %x", got, plaintext)
			}
		})
	}
}

func TestCipherLeavesInputUnchanged(t *testing.T) {
	plaintext := []byte(sysinfoRequest)
	Encrypt(plaintext)
	if string(plaintext) != sysinfoRequest {
		t.Errorf("Encrypt modified its input: %q", plaintext)
	}

	ciphertext := sysinfoCiphertext(t)
	Decrypt(ciphertext)
	if !bytes.Equal(ciphertext, sysinfoCiphertext(t)) {
		t.Errorf("Decrypt modified its input: %x", ciphertext)
	}
}

func FuzzCipherRoundTrip(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte(sysinfoRequest))
	f.Add([]byte{0x00, 0xAB, 0xFF})
	f.Fuzz(func(t *testing.T, plaintext []byte) {
		if got := Decrypt(Encrypt(plaintext)); !bytes.Equal(got, plaintext) {
			t.Errorf("round trip\n got %x\nwant %x", got, plaintext)
		}
	})
}
