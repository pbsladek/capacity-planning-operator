package cirunner

import (
	"regexp"
	"testing"
)

func TestParseExtraImages(t *testing.T) {
	got := parseExtraImages("busybox:1.36, python:3.12-alpine  redis:7")
	want := []string{"busybox:1.36", "python:3.12-alpine", "redis:7"}
	if len(got) != len(want) {
		t.Fatalf("got=%v want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("index %d got=%q want=%q", i, got[i], want[i])
		}
	}
}

func TestCandidateImageRefs(t *testing.T) {
	refs := candidateImageRefs("busybox:1.36")
	if len(refs) < 3 {
		t.Fatalf("unexpected refs: %v", refs)
	}
	if refs[0] != "busybox:1.36" {
		t.Fatalf("unexpected refs[0]=%q", refs[0])
	}
}

func TestCandidateImagePatternsMatch(t *testing.T) {
	patterns := candidateImagePatterns("busybox:1.36")
	if len(patterns) == 0 {
		t.Fatalf("expected patterns")
	}
	matchAny := false
	for _, p := range patterns {
		re := regexp.MustCompile(p)
		if re.MatchString("docker.io/library/busybox:1.36") || re.MatchString("busybox:1.36") {
			matchAny = true
			break
		}
	}
	if !matchAny {
		t.Fatalf("patterns did not match expected refs: %v", patterns)
	}
}

func TestNodeHasImageExactAndPattern(t *testing.T) {
	images := []string{"docker.io/library/busybox:1.36", "ghcr.io/x/y:1"}
	if !nodeHasImage(images, []string{"docker.io/library/busybox:1.36"}, nil) {
		t.Fatalf("expected exact match")
	}
	if !nodeHasImage(images, nil, []string{`(^|.*/)busybox:1\.36$`}) {
		t.Fatalf("expected pattern match")
	}
	if nodeHasImage(images, []string{"alpine:3"}, []string{`alpine:3`}) {
		t.Fatalf("unexpected match")
	}
}

func TestRunImportImageK3DRequiresEnv(t *testing.T) {
	t.Setenv("CLUSTER_NAME", "")
	t.Setenv("OPERATOR_IMAGE", "")
	err := RunImportImageK3D(t.Context(), Config{})
	if err == nil || err.Error() != "CLUSTER_NAME must be set" {
		t.Fatalf("unexpected err: %v", err)
	}

	t.Setenv("CLUSTER_NAME", "cpo-ci")
	t.Setenv("OPERATOR_IMAGE", "")
	err = RunImportImageK3D(t.Context(), Config{})
	if err == nil || err.Error() != "OPERATOR_IMAGE must be set" {
		t.Fatalf("unexpected err: %v", err)
	}
}
