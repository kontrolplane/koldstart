# security policy

## reporting a vulnerability

do **not** open a public issue. report it through a [github security advisory](https://github.com/kontrolplane/koldstart/security/advisories/new) with:

- what the vulnerability is and its impact.
- steps to reproduce.
- the affected version or commit.

we reply within a few business days. please wait for a fix before disclosing.

## threat model

koldstart runs with credentials that can create, change and delete what a configuration describes, so whoever controls what koldstart deploys controls that account. koldstart is in the design phase: this is the model [#1](https://github.com/kontrolplane/koldstart/issues/1) builds to.

- koldstart talks only to the aws apis of the account it deploys to. it downloads the pinned pkl binary and the schema package from github releases, and sends no telemetry.
- the state bucket, `koldstart-state-<account>-<region>`, holds the state, locks and zips of every app and stage in that account and region. whoever can write to it can change what the next deploy does: give write access only to the roles that deploy.
- every call to the state bucket passes the expected bucket owner, so a bucket of the same name in another account is refused, not used.
- resolved secrets never go into the state. `secretsAtRuntime = true` keeps them out of the function's configuration too.

## verifying releases

the archives carry github build provenance over `checksums.txt`:

```sh
gh attestation verify koldstart_<version>_<os>_<arch>.tar.gz --repo kontrolplane/koldstart
```

images are signed in CI with cosign, keyless, and carry provenance and an SBOM:

```sh
cosign verify ghcr.io/kontrolplane/koldstart:<tag> \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github.com/kontrolplane/koldstart/\.github/workflows/image\.yaml@refs/(heads/main|tags/v.+)$'
```

## supported versions

pre-1.0. fixes land on `main` only.
