package civerify

import (
	"context"
	"strings"
	"testing"
)

func TestCompareGrowthAllMatched(t *testing.T) {
	status := []PVCGrowth{
		{Name: "a", StatusBytesPerDay: 100},
		{Name: "b", StatusBytesPerDay: 200},
	}
	opts := CompareOptions{
		RelativeTolerance:    0.2,
		AbsToleranceBytesDay: 1,
		MinComparablePVCs:    2,
		MinMatchingPVCs:      2,
	}

	summary, err := CompareGrowth(context.Background(), status, func(_ context.Context, pvcName string) (float64, bool, error) {
		switch pvcName {
		case "a":
			return 110, true, nil
		case "b":
			return 190, true, nil
		default:
			return 0, false, nil
		}
	}, opts)
	if err != nil {
		t.Fatalf("CompareGrowth returned error: %v", err)
	}
	if summary.Comparable != 2 {
		t.Fatalf("expected 2 comparable PVCs, got %d", summary.Comparable)
	}
	if summary.Matched != 2 {
		t.Fatalf("expected 2 matched PVCs, got %d", summary.Matched)
	}
	for _, row := range summary.Rows {
		if !row.Matched {
			t.Fatalf("expected row %s to match", row.Name)
		}
		if row.Reason != "within_tolerance" {
			t.Fatalf("expected within_tolerance reason for %s, got %q", row.Name, row.Reason)
		}
		if row.ToleranceBasis == "" {
			t.Fatalf("expected tolerance basis for %s", row.Name)
		}
	}
}

func TestCompareGrowthInsufficientComparable(t *testing.T) {
	status := []PVCGrowth{
		{Name: "a", StatusBytesPerDay: 100},
		{Name: "b", StatusBytesPerDay: 200},
		{Name: "c", StatusBytesPerDay: 300},
	}
	opts := CompareOptions{
		RelativeTolerance:    0.3,
		AbsToleranceBytesDay: 1,
		MinComparablePVCs:    3,
		MinMatchingPVCs:      2,
	}

	summary, err := CompareGrowth(context.Background(), status, func(_ context.Context, pvcName string) (float64, bool, error) {
		switch pvcName {
		case "a":
			return 110, true, nil
		case "b":
			return 190, true, nil
		default:
			return 0, false, nil
		}
	}, opts)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "only 2 comparable PVCs") {
		t.Fatalf("unexpected error: %v", err)
	}
	if summary.Comparable != 2 {
		t.Fatalf("expected comparable=2, got %d", summary.Comparable)
	}
}

func TestCompareGrowthInsufficientMatches(t *testing.T) {
	status := []PVCGrowth{
		{Name: "a", StatusBytesPerDay: 100},
		{Name: "b", StatusBytesPerDay: 200},
	}
	opts := CompareOptions{
		RelativeTolerance:    0.05,
		AbsToleranceBytesDay: 1,
		MinComparablePVCs:    2,
		MinMatchingPVCs:      2,
	}

	summary, err := CompareGrowth(context.Background(), status, func(_ context.Context, pvcName string) (float64, bool, error) {
		switch pvcName {
		case "a":
			return 103, true, nil
		case "b":
			return 260, true, nil
		default:
			return 0, false, nil
		}
	}, opts)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "matched 1/2 PVCs") {
		t.Fatalf("unexpected error: %v", err)
	}
	if summary.Matched != 1 {
		t.Fatalf("expected matched=1, got %d", summary.Matched)
	}
	var badRow ComparisonRow
	found := false
	for _, row := range summary.Rows {
		if row.Name == "b" {
			badRow = row
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected row for pvc b")
	}
	if badRow.Reason == "" || badRow.Reason == "within_tolerance" {
		t.Fatalf("expected non-matching reason for pvc b, got %q", badRow.Reason)
	}
}

func TestBuildPVCGrowthDerivQuery(t *testing.T) {
	got := BuildPVCGrowthDerivQuery("default", "my-pvc", 240)
	want := `max(deriv(kubelet_volume_stats_used_bytes{namespace="default",persistentvolumeclaim="my-pvc"}[240s])) * 86400`
	if got != want {
		t.Fatalf("unexpected query:\nwant: %s\ngot:  %s", want, got)
	}
}
