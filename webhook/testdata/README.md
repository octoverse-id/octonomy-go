# Octonomy webhook signature vectors

`signature_vectors.json` holds known-good HMAC-SHA256 signature vectors for
Octonomy webhook deliveries, plus the deliveries that must be **rejected** and
why.

**These vectors are portable and they are meant to be reused.** Nothing in the
file is Go-specific and nothing in it depends on a payload shape — only on the
signature contract, which is fixed. An SDK in any language can drive its
verifier from this one file, and should: every consumer that derives the
contract from the server's Python instead is one more place to get it subtly
wrong.

## The contract

The server signs the **exact bytes of the request body** with HMAC-SHA256 under
the shared signing secret, hex-encodes the digest in lowercase, and sends it
prefixed with `sha256=`:

```
X-Octonomy-Signature: sha256=<hex hmac-sha256(utf8(secret), raw_body)>
```

Source of truth: `octonomy/events/dispatch.py::_webhook_signature` in the
Octonomy server.

## Provenance

The digests here were **not** produced by the code they test. They come from
`generate_vectors.py`, which runs the same three lines the server runs, and
every accept vector was independently confirmed against `openssl dgst -sha256
-hmac`. A vector computed by the implementation under test proves only that the
implementation agrees with itself.

Regenerate with:

```sh
python3 generate_vectors.py > signature_vectors.json
```

The output is deterministic — a regeneration that changes a digest means
something about the contract changed, and that is worth a hard look rather than
a commit.

## Format

```jsonc
{
  "version": 1,                       // bump if the shape below changes
  "algorithm": "HMAC-SHA256",
  "header": "X-Octonomy-Signature",
  "format": "sha256=<lowercase hex digest of HMAC-SHA256(utf8(secret), raw_body)>",
  "accept": [ /* must verify */ ],
  "reject": [ /* must NOT verify */ ]
}
```

Every vector carries:

| Field | Meaning |
| --- | --- |
| `name` | Stable identifier. Use it to name the subtest, so a failure says which case. |
| `description` | What the case is for, and what a wrong implementation does with it. |
| `secret` | The signing secret, as **UTF-8 text**. Encode it to bytes before using it as the HMAC key. |
| `body` | The request body as a UTF-8 string. |
| `body_base64` | Present **instead of** `body` when the body is not valid UTF-8. Base64-decode it; those bytes are the body. Exactly one of the two is present. |
| `signature` | The full header value, prefix included. |
| `reason` | **Reject vectors only.** Why it must be refused — see below. |

A verifier must handle both `body` and `body_base64`: HMAC is over bytes, and a
verifier whose body parameter is a string cannot express the
`non-utf8-binary-body` case at all. That is itself worth knowing about an SDK.

## Reject reasons

`reason` is language-neutral. Map it to whatever your SDK returns; the point is
that each one is refused **and** that the refusals are told apart, since a
verifier whose failures are indistinguishable cannot tell an operator whether it
is misconfigured or under attack.

| `reason` | Meaning |
| --- | --- |
| `missing_header` | No signature header at all. |
| `unsupported_algorithm` | The value does not begin with the exact prefix `sha256=`. Includes a bare hex digest, `SHA256=`, and a digest announced as another algorithm. |
| `malformed_digest` | A `sha256=` value whose digest is not 64 hex characters: wrong length, non-hex, or empty. |
| `digest_mismatch` | A well-formed digest that is not the right one — a forgery, the wrong secret, or a body altered or truncated after signing. |
| `empty_body` | **Policy, not cryptography.** See below. |
| `empty_secret` | **Policy, not cryptography.** See below. |

### The two policy reasons

`empty_body` and `empty_secret` are the only vectors whose signatures are
cryptographically **valid**. They are refused anyway, and an SDK that chooses to
accept them is not wrong about the math — but it should make that choice
deliberately.

- **`empty_body`** — the HMAC of zero bytes is well defined, and
  `empty-body-correct-digest` records the correct digest for it. Octonomy never
  sends an empty body, so zero bytes at a verifier overwhelmingly means the
  request body was **already read** by middleware, a logger, or a JSON decoder.
  Left as an ordinary mismatch, that surfaces as a check that appears to run and
  fails on every delivery forever — which someone eventually "fixes" by deleting
  it. `octonomy-go` names it instead.
- **`empty_secret`** — an unset environment variable arrives as `""`, and HMAC
  under an empty key verifies perfectly. The key is then one every attacker also
  has, so every forged delivery passes while the handler looks like it is
  checking signatures.

## What these vectors do not cover

**Replay.** The server sends no timestamp header, so there is no signed
freshness claim and no window to enforce; a captured delivery replayed later
carries a signature that is still valid, and no vector can say otherwise.
Delivery is also at-least-once, so genuine redeliveries happen on their own.
Deduplicate on the envelope's stable `id` and make handlers idempotent.

**Payload shape.** Nothing here asserts what is inside the body. That is
deliberate: it is what keeps these vectors valid as event payloads evolve.
