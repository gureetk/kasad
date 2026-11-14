import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { test } from 'node:test';

import { decrypt, encrypt, hex, lengthPrefix } from '../static/protocol.js';

// The same vector as protocol/cipher_test.go.
const SYSINFO = '{"system":{"get_sysinfo":{}}}';
const SYSINFO_WIRE = 'd0 f2 81 f8 8b ff 9a f7 d5 ef 94 b6 d1 b4 c0 9f ec 95 e6 8f e1 87 e8 ca f0 8b f6 8b f6';

test('encrypt gives the same bytes as the protocol package', () => {
  assert.equal(hex(encrypt(SYSINFO)), SYSINFO_WIRE);
});

test('decrypt undoes encrypt', () => {
  for (const text of [SYSINFO, '{"system":{"set_relay_state":{"state":1}}}', '', 'Küche, 台所']) {
    assert.equal(decrypt(encrypt(text)), text);
  }
});

test('lengthPrefix is four bytes, most significant first', () => {
  assert.equal(hex(lengthPrefix(29)), '00 00 00 1d');
  assert.equal(hex(lengthPrefix(0x01020304)), '01 02 03 04');
  assert.equal(hex(lengthPrefix(0)), '00 00 00 00');
});

// The page shows the first command before its script runs, so what is
// written in the HTML has to match what the script would show.
test('the page starts out showing the right bytes', async () => {
  const html = await readFile(new URL('../static/index.html', import.meta.url), 'utf8');
  const shown = (name) => {
    const found = html.match(new RegExp(`data-out="${name}"[^>]*>([^<]*)<`));
    assert.ok(found, `no element with data-out="${name}"`);
    return found[1];
  };
  const json = shown('json').replaceAll('&quot;', '"');
  assert.equal(json, SYSINFO);
  assert.equal(shown('bytes'), SYSINFO_WIRE);
  assert.equal(shown('length'), '00 00 00 1d');
  assert.equal(shown('count'), '29 bytes of JSON follow');
  const first = html.match(/data-json='([^']*)'[^>]*aria-pressed="true"/);
  assert.ok(first, 'no command is marked as chosen');
  assert.equal(first[1], SYSINFO);
});
