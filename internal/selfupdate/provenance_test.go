package selfupdate

import (
	"slices"
	"testing"
)

func TestNeedsProvenance(t *testing.T) {
	for tag, want := range map[string]bool{
		"v0.1.0": false, "v0.3.0": false, "v0.3.9": false, "v0.4.0-rc.1": true,
		"v0.4.0": true, "0.4.0": true, "v0.4.1": true, "v1.0.0": true, "junk": true,
	} {
		if got := NeedsProvenance(tag); got != want {
			t.Errorf("NeedsProvenance(%q) = %v, want %v", tag, got, want)
		}
	}
	defer func(old string) { ProvenanceFrom = old }(ProvenanceFrom)
	ProvenanceFrom = ""
	if NeedsProvenance("v9.0.0") {
		t.Error("ProvenanceFrom empty: still needed")
	}
}

func TestAttestArgs(t *testing.T) {
	got := AttestArgs("/tmp/x/pekoe_0.4.0_darwin_arm64.tar.gz", "sakamosu/pekoe", "v0.4.0")
	want := []string{
		"attestation", "verify", "/tmp/x/pekoe_0.4.0_darwin_arm64.tar.gz",
		"-R", "sakamosu/pekoe",
		"--signer-workflow", "sakamosu/pekoe/.github/workflows/release.yml",
		"--source-ref", "refs/tags/v0.4.0",
		"--deny-self-hosted-runners",
	}
	if !slices.Equal(got, want) {
		t.Errorf("AttestArgs\n got %q\nwant %q", got, want)
	}
}
