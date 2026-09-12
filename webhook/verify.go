package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Headers on an Octonomy webhook delivery.
//
// HeaderRequestID is CONDITIONAL -- the server sets it only for an event that
// carries a request id -- so its absence is ordinary and not a malformed
// delivery. The other four are always present.
//
// NONE OF THE FOUR NON-SIGNATURE HEADERS MAY DRIVE A DECISION, before or after
// verification. The signature covers the BODY and nothing else, so they are
// never authenticated -- not even by a Verify that returned nil. A captured,
// entirely genuine delivery can be replayed with X-Octonomy-Tenant-ID rewritten
// and its signature still checks out, so routing, partitioning, or authorizing
// on one of them is how a real event reaches the wrong tenant's handler.
//
// Read tenant, event id, and event type from the PARSED BODY, which the
// signature does cover. These constants exist to name the headers the server
// documents and to read them for logging and correlation while diagnosing a
// delivery -- as untrusted strings, on the same footing as anything else the
// sender chose to send.
const (
	HeaderSignature = "X-Octonomy-Signature"
	HeaderEventID   = "X-Octonomy-Event-ID"
	HeaderEventType = "X-Octonomy-Event-Type"
	HeaderTenantID  = "X-Octonomy-Tenant-ID"
	HeaderRequestID = "X-Octonomy-Request-ID"
)

// signaturePrefix is the algorithm label the server writes ahead of the hex
// digest. It is matched exactly: the server emits it lowercase, and an
// implementation that also accepted "SHA256=" or "sha512=" would be letting the
// SENDER choose the algorithm, which is the sender an attacker controls.
const signaturePrefix = "sha256="

// Errors returned by [Verify]. Every one of them is a refusal, and the whole
// point of having six is that none of them can be mistaken for success: a
// caller who writes `if err := Verify(...); err != nil` is correct, and a
// caller who wants to tell a misconfiguration from a forgery can.
//
// They divide into three groups, and the group is the operator's response:
//
//   - ErrNoSecret and ErrEmptyBody are CALLER errors: the verification never
//     ran because it was not given what it needs. Nothing about the sender is
//     established, and no retry will fix it. Fix the deployment.
//   - ErrMissingSignature, ErrUnsupportedAlgorithm, and ErrMalformedSignature
//     are GRAMMAR errors: the request does not carry a signature this package
//     can even compare. Octonomy never sends one of these, so the sender is
//     either not Octonomy or something rewrote the header in transit.
//   - ErrSignatureMismatch is the CRYPTOGRAPHIC verdict: a well-formed digest
//     that is not the right one. A forgery, a body altered in transit, a body
//     the handler had already partly consumed, or the wrong secret.
//
// Use [errors.Is]; the malformed cases wrap ErrMalformedSignature with a
// description of what was wrong with the digest.
var (
	// ErrNoSecret reports that the secret passed to Verify was empty.
	//
	// This is a refusal, not a verification. HMAC under an empty key is
	// perfectly well defined, which is exactly the danger: the key would be one
	// every attacker already knows, so every forged delivery would verify and
	// the handler would look like it was checking signatures. An unset
	// environment variable reaching Verify as "" is the realistic way that
	// happens, and it is not a state this package will quietly operate in.
	ErrNoSecret = errors.New("octonomy: webhook signing secret is empty")

	// ErrMissingSignature reports an empty signature header: the delivery
	// carries no signature at all. An unsigned POST to a webhook endpoint is
	// the first thing anyone who finds the URL will try.
	ErrMissingSignature = errors.New("octonomy: webhook signature header is missing")

	// ErrUnsupportedAlgorithm reports a signature header that does not begin
	// with the exact prefix "sha256=" -- a bare hex digest, an uppercase
	// "SHA256=", or a digest announced as some other algorithm.
	//
	// It is distinct from ErrMalformedSignature because the fix is different:
	// this one usually means the sender is not Octonomy, or that a proxy
	// rewrote the header, rather than that a digest got corrupted.
	ErrUnsupportedAlgorithm = errors.New("octonomy: webhook signature is not a " + signaturePrefix + " digest")

	// ErrMalformedSignature reports a "sha256=" header whose digest is not 64
	// hexadecimal characters: wrong length, non-hex characters, or nothing at
	// all after the prefix. Errors from this case wrap it with the specific
	// defect.
	//
	// Length is checked rather than left to the comparison because a
	// length-blind comparison can treat a short digest as a prefix match, and
	// because "the digest is truncated" and "the digest is wrong" are different
	// operational stories.
	ErrMalformedSignature = errors.New("octonomy: webhook signature digest is malformed")

	// ErrSignatureMismatch reports a well-formed digest that is not the digest
	// of this body under this secret.
	//
	// The body was not signed by a holder of the secret, OR it is not the body
	// that was signed. Those cannot be told apart and do not need to be: both
	// mean the delivery must be refused. A handler that starts seeing this on
	// EVERY delivery is usually not under attack -- it is verifying a body
	// something else already read. See ErrEmptyBody.
	ErrSignatureMismatch = errors.New("octonomy: webhook signature does not match the body")

	// ErrEmptyBody reports that Verify was handed zero bytes.
	//
	// This is a policy refusal and not a cryptographic one: HMAC of the empty
	// message is well defined, and a correct digest for it exists. Octonomy
	// never sends an empty body -- every delivery carries a serialized event --
	// so zero bytes at this point overwhelmingly means the request body was
	// ALREADY READ: by middleware, by a logger, by a json.Decoder, by an
	// io.ReadAll whose result went somewhere else.
	//
	// Without this, that case would surface as ErrSignatureMismatch on every
	// single delivery forever -- a check that appears to run and always fails,
	// which someone eventually "fixes" by deleting it. Naming it is what turns
	// a mysterious permanent failure into a one-line fix.
	ErrEmptyBody = errors.New("octonomy: webhook body is empty")
)

// Verify reports whether body carries a valid Octonomy webhook signature.
//
// secret is the deployment's OCTONOMY_WEBHOOK_SIGNING_SECRET.
// signatureHeader is the raw value of the [HeaderSignature] header, prefix and
// all -- pass r.Header.Get(webhook.HeaderSignature) verbatim rather than
// trimming it. body is the exact bytes of the request body.
//
// A nil return means those bytes were signed with that secret. It means nothing
// about freshness: see the package documentation on replay, which this package
// cannot prevent and which the server gives it no way to check. Any non-nil
// return means the delivery must be refused; the errors above say why.
//
// # Why []byte and not *http.Request
//
// The signature covers the RAW BYTES. Anything that reads the request body
// first -- a logging middleware, a body-copying tracer, json.NewDecoder(r.Body)
// -- leaves Verify hashing an empty or partial body, so the check silently
// operates on the wrong input while appearing to run. Taking bytes that the
// caller has already read makes handing this function an unread stream
// structurally impossible, and leaves the read (and its size limit, which
// Verify cannot impose from here) where the caller can see it.
//
// # Bounding the body is the caller's job
//
// This SDK ships no http.Handler, so nothing bounds the read for you. Wrap the
// body in [net/http.MaxBytesReader] before reading it; by the time Verify has
// bytes, an unbounded body has already been allocated.
//
// # Comparison
//
// The digests are compared with [crypto/hmac.Equal], in constant time, after
// the received hex is decoded -- never as hex strings and never with ==, both
// of which leak how far a forged digest got through a byte-by-byte comparison.
// Because the comparison is over decoded bytes, the case of the hex is
// irrelevant and an uppercase digest verifies; the "sha256=" prefix is still
// matched exactly, since that names the algorithm rather than encoding bytes.
func Verify(secret string, signatureHeader string, body []byte) error {
	// Ordered so the error names the most specific thing that is wrong. The
	// configuration error comes first because it is true regardless of what the
	// sender did; "is this signed at all" comes before "did the bytes survive",
	// so ErrEmptyBody is reserved for a delivery that WAS signed and still
	// arrived with nothing in it -- which is the drained-stream diagnosis.
	if secret == "" {
		return ErrNoSecret
	}
	if signatureHeader == "" {
		return ErrMissingSignature
	}
	if len(body) == 0 {
		return ErrEmptyBody
	}

	digestHex, ok := strings.CutPrefix(signatureHeader, signaturePrefix)
	if !ok {
		return ErrUnsupportedAlgorithm
	}
	if want := hex.EncodedLen(sha256.Size); len(digestHex) != want {
		return fmt.Errorf("%w: expected %d hex characters, got %d", ErrMalformedSignature, want, len(digestHex))
	}
	received, err := hex.DecodeString(digestHex)
	if err != nil {
		// The offending value is deliberately not echoed: it is unbounded,
		// attacker-controlled bytes, and it goes straight into someone's logs.
		return fmt.Errorf("%w: digest is not hexadecimal", ErrMalformedSignature)
	}

	mac := hmac.New(sha256.New, []byte(secret))
	// hash.Hash documents that Write never returns an error.
	_, _ = mac.Write(body)
	if !hmac.Equal(mac.Sum(nil), received) {
		return ErrSignatureMismatch
	}
	return nil
}
