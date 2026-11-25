### Security

- **Go toolchain bumped to 1.27.4 and `golang.org/x/net` to v0.63.0.** The module's `go` line
  (`go.mod`) and the runtime image's builder stage (`golang:1.27.4-alpine3.24` in the
  `Dockerfile`) both move, which clears the six HIGH findings Trivy reported against the
  published image: CVE-2026-78661 and CVE-2026-78669 in `golang.org/x/net` (denial of service
  through crafted HTML and through excessive HTTP/2 SETTINGS frames), and CVE-2026-111943
  (`crypto/x509`), CVE-2026-78667 (`net/http` Range headers), CVE-2026-78669 and CVE-2026-97031
  (`crypto/tls`) in the standard library. The server's listener and the outbound OIDC transport
  both reach the affected HTTP/2 path. `golang.org/x/crypto`, `x/sys`, `x/text` and the indirect
  `x/` modules move with `x/net`'s own requirements. The final runtime stage is unchanged.
- **`github.com/klauspost/compress` lifted to v1.20.4**, closing GO-2026-6865, which govulncheck
  reported as reachable.

### Internal

- **staticcheck, golangci-lint and gosec are now built against a pinned `golang.org/x/tools`
  (v0.52.0)** through `.github/actions/go-install-on-xtools`, as upstream does. The analyzers read
  the standard library's export data through x/tools, and Go 1.27.4 writes a format their own
  pins cannot read: staticcheck v0.8.1 failed on every package ("export data version 5 is
  greater than maximum supported version 4") and gosec v2.29.0 reported "package has type errors".
  The Go-tools cache key carries the x/tools version so no stale binary is restored.
- **Three npm dev dependencies lifted in the lockfile** (`npm update --package-lock-only`), clearing
  the ten HIGH findings `trivy-fs` reported in `package-lock.json`: `undici` 8.10.0 → 8.13.1 (via
  `jsdom`; CVE-2026-19534, -84961, -85152, -87018, -109383, -109829, -109869),
  `brace-expansion` 5.0.9 → 5.1.0 (via `minimatch`; CVE-2026-102276, -102278) and `source-map-js`
  1.2.1 → 1.2.4 (via `@tailwindcss/node` and `css-tree`; CVE-2026-93749). All three are within the
  ranges their dependents already declare; nothing is shipped from them.

### Internal

- **`export --out` carries a `#nosec G304` annotation.** `writeExportFile` opens the path the
  operator typed on the command line; the finding was a true positive about the shape of the call
  and a false positive about its trust boundary, as with `internal/security/local_file.go`.
- **`coverage-upload` runs in `ovumcy/ovumcy-web` only.** Codecov has no project for a fork and
  answers the upload with "Repository not found", which failed the lane on every same-repo pull
  request here; no gate reads this job's result.
- **`publish-image` publishes from `ovumcy/ovumcy-web` only.** `ghcr.io/<owner>/<repo>` is the
  package it pushes; from a fork that is a package nobody pulls, created private on first push,
  after which the job's anonymous verification of the public tags fails. Building and scanning the
  image (`image-smoke`, `trivy-image`) is unchanged everywhere.
