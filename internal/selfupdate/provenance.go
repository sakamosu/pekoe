package selfupdate

// ProvenanceFrom is the first release that must carry a build provenance
// (an attestation made by release.yml): pekoe update stops when one from
// here on has none, or when `gh attestation verify` rejects it. Releases
// before it (v0.1.0 to v0.3.0, made while the repository was private) have
// none, and only get a warning. A build for a fork that does not attest its
// releases can turn the requirement off:
//
//	-ldflags "-X pekoe/internal/selfupdate.ProvenanceFrom="
var ProvenanceFrom = "v0.4.0"

// SignerWorkflow is the workflow (in Repo) that attests releases.
const SignerWorkflow = ".github/workflows/release.yml"

// NeedsProvenance reports whether the release tag must carry a build
// provenance. Prereleases of ProvenanceFrom and later count too (release.yml
// attests them alike), so only the version core is compared.
func NeedsProvenance(tag string) bool {
	if ProvenanceFrom == "" {
		return false
	}
	from, ok := ParseVersion(ProvenanceFrom)
	if !ok {
		return false
	}
	v, ok := ParseVersion(tag)
	if !ok {
		// not a version: pekoe update refuses it before this anyway
		return true
	}
	v.Pre, v.Ahead = "", 0
	from.Pre, from.Ahead = "", 0
	return Compare(v, from) >= 0
}

// AttestArgs are the arguments of `gh` that verify file's build provenance:
// signed for repo, by repo's release.yml, building the tag (not a branch or
// another tag), on a runner GitHub hosts.
func AttestArgs(file, repo, tag string) []string {
	return []string{
		"attestation", "verify", file,
		"-R", repo,
		"--signer-workflow", repo + "/" + SignerWorkflow,
		"--source-ref", "refs/tags/" + tag,
		"--deny-self-hosted-runners",
	}
}
