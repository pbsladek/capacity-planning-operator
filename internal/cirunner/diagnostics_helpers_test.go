package cirunner

import "testing"

func TestNamespacedResourcesTextSorts(t *testing.T) {
	got := namespacedResourcesText([]string{"b", "a"})
	if got != "a\nb\n" {
		t.Fatalf("got %q", got)
	}
	if namespacedResourcesText(nil) != "\n" {
		t.Fatalf("expected newline for empty")
	}
}
