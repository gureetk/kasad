// The plug's framing and cipher, as the protocol package does them, for the
// demonstration on the page. A message is a 4-byte big-endian length followed
// by the JSON, scrambled with an autokey XOR: the key starts at 171, and after
// that each byte's key is the byte that came out before it.

export const INITIAL_KEY = 0xab;

// encrypt returns the bytes a client sends for text, without the length.
export function encrypt(text) {
  const plain = new TextEncoder().encode(text);
  const out = new Uint8Array(plain.length);
  let key = INITIAL_KEY;
  for (let i = 0; i < plain.length; i++) {
    key ^= plain[i];
    out[i] = key;
  }
  return out;
}

// decrypt undoes encrypt. A reply from the plug is read the same way.
export function decrypt(bytes) {
  const plain = new Uint8Array(bytes.length);
  let key = INITIAL_KEY;
  for (let i = 0; i < bytes.length; i++) {
    plain[i] = bytes[i] ^ key;
    key = bytes[i];
  }
  return new TextDecoder().decode(plain);
}

// lengthPrefix returns the four bytes that go before a message of n bytes.
export function lengthPrefix(n) {
  return Uint8Array.of((n >>> 24) & 0xff, (n >>> 16) & 0xff, (n >>> 8) & 0xff, n & 0xff);
}

// hex writes bytes as two-digit hexadecimal numbers separated by spaces.
export function hex(bytes) {
  return Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join(' ');
}
