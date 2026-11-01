# Releases are signed with a key compiled into ctg

`ctg update` checks an ed25519 signature over SHA256SUMS and refuses a release that does not verify. The public key is compiled in. The private key is a GitHub Actions secret, so the builder never sees it. A key change ships inside a signed update.

## Considered options

- Signing only on the owner's machine. Stronger against a stolen GitHub account, and left for later. A stolen account can still produce a signature with the current setup.
