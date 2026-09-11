package octonomy

// Version is the SemVer release of this SDK. It is the single source of truth for
// the SDK version and is verified against CHANGELOG.md by `make version-check`.
//
// The SDK versions independently of the Octonomy server; see docs/versioning.md.
const Version = "2.0.0-alpha.1"

// defaultUserAgent identifies the SDK on outbound requests. Override it with
// Config.UserAgent.
const defaultUserAgent = "octonomy-go/" + Version
