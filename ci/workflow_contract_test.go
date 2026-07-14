package ci

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

const sharedPublisherSHA = "a86300fb8020d0f7141bb9f833d89b5dbd7aa4d7"

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	return filepath.Dir(filepath.Dir(current))
}

func readRepositoryFile(t *testing.T, path ...string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(append([]string{repositoryRoot(t)}, path...)...))
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Join(path...), err)
	}
	return string(content)
}

func TestImagePublicationUsesGuardedSharedContract(t *testing.T) {
	workflow := readRepositoryFile(t, ".github", "workflows", "lint-test-build.yml")
	for _, required := range []string{
		"libops/.github/.github/workflows/build-push.yaml@" + sharedPublisherSHA,
		"additional-gar-registry: us-docker.pkg.dev/libops-images/public",
		"expected-main-sha:",
		"scan: true",
		"sign: true",
		"certificate-identity: https://github.com/libops/.github/.github/workflows/build-push.yaml@" + sharedPublisherSHA,
		"GCLOUD_OIDC_POOL: ${{ secrets.GCLOUD_OIDC_POOL }}",
		"GSA: ${{ secrets.GSA }}",
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("image workflow must contain %q", required)
		}
	}
	if strings.Contains(workflow, "secrets: inherit") {
		t.Fatal("image workflow must map only its required registry secrets")
	}
}

func TestReleaseImageCallMapsOnlyRegistrySecrets(t *testing.T) {
	workflow := readRepositoryFile(t, ".github", "workflows", "goreleaser.yml")
	if strings.Contains(workflow, "secrets: inherit") {
		t.Fatal("release workflow must not pass every repository secret")
	}
	for _, required := range []string{
		"if: github.ref_type == 'tag'",
		"uses: ./.github/workflows/lint-test-build.yml",
		"CODECOV_TOKEN: ${{ secrets.CODECOV_TOKEN }}",
		"GCLOUD_OIDC_POOL: ${{ secrets.GCLOUD_OIDC_POOL }}",
		"GSA: ${{ secrets.GSA }}",
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("release workflow must contain %q", required)
		}
	}
}

func TestDockerfilePinsPublishedBuildkitImages(t *testing.T) {
	dockerfile := readRepositoryFile(t, "Dockerfile")
	for _, pattern := range []string{
		`FROM ghcr\.io/libops/go:[^\s]+@sha256:[0-9a-f]{64} AS builder`,
		`FROM ghcr\.io/libops/base:[0-9][^\s]*@sha256:[0-9a-f]{64}`,
	} {
		if !regexp.MustCompile(pattern).MatchString(dockerfile) {
			t.Errorf("Dockerfile must match %q", pattern)
		}
	}
	if strings.Contains(dockerfile, "ghcr.io/libops/base:main") {
		t.Fatal("runtime base must use a released version tag")
	}
}
