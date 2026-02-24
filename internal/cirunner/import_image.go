package cirunner

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

func parseExtraImages(raw string) []string {
	out := make([]string, 0, 8)
	for p := range strings.FieldsFuncSeq(raw, func(r rune) bool {
		return r == ',' || unicode.IsSpace(r)
	}) {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

func commandOutput(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		if strings.TrimSpace(stderr.String()) != "" {
			return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return "", err
	}
	return out.String(), nil
}

func commandRun(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func discoverK3DNodes(ctx context.Context, clusterName string) ([]string, error) {
	out, err := commandOutput(ctx, "k3d", "node", "list", "--no-headers")
	if err != nil {
		return nil, fmt.Errorf("k3d node list failed: %w", err)
	}
	re := regexp.MustCompile(`^k3d-` + regexp.QuoteMeta(clusterName) + `-(server|agent)-[0-9]+$`)
	nodes := make([]string, 0)
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		first := ""
		for field := range strings.FieldsSeq(line) {
			first = field
			break
		}
		if first == "" {
			continue
		}
		if re.MatchString(first) {
			nodes = append(nodes, first)
		}
	}
	sort.Strings(nodes)
	if len(nodes) == 0 {
		return nil, fmt.Errorf("no k3d nodes found for cluster %s", clusterName)
	}
	return nodes, nil
}

func candidateImageRefs(img string) []string {
	refs := []string{img}
	first := img
	if i := strings.Index(first, "/"); i >= 0 {
		first = first[:i]
	}
	if !strings.Contains(img, "/") {
		refs = append(refs, "docker.io/library/"+img, "index.docker.io/library/"+img)
		return refs
	}
	if !strings.Contains(first, ".") && !strings.Contains(first, ":") && first != "localhost" {
		refs = append(refs, "docker.io/"+img, "index.docker.io/"+img)
	}
	return refs
}

func candidateImagePatterns(img string) []string {
	ref := img
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	base := ref
	tag := ""
	last := ref
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		last = ref[i+1:]
	}
	if strings.Contains(last, ":") {
		idx := strings.LastIndex(ref, ":")
		base = ref[:idx]
		tag = ref[idx+1:]
	}
	if tag == "" {
		return []string{img}
	}
	name := base
	if i := strings.LastIndex(base, "/"); i >= 0 {
		name = base[i+1:]
	}
	patterns := []string{
		fmt.Sprintf("^%s:%s$", regexp.QuoteMeta(base), regexp.QuoteMeta(tag)),
		fmt.Sprintf("^docker\\.io/%s:%s$", regexp.QuoteMeta(base), regexp.QuoteMeta(tag)),
		fmt.Sprintf("^index\\.docker\\.io/%s:%s$", regexp.QuoteMeta(base), regexp.QuoteMeta(tag)),
		fmt.Sprintf(`(^|.*/)%s:%s(@sha256:[a-f0-9]+)?$`, regexp.QuoteMeta(name), regexp.QuoteMeta(tag)),
	}
	if !strings.Contains(base, "/") {
		patterns = append(patterns,
			fmt.Sprintf("^docker\\.io/library/%s:%s$", regexp.QuoteMeta(name), regexp.QuoteMeta(tag)),
			fmt.Sprintf("^index\\.docker\\.io/library/%s:%s$", regexp.QuoteMeta(name), regexp.QuoteMeta(tag)),
		)
	}
	return patterns
}

func nodeImageList(ctx context.Context, node string) ([]string, string, error) {
	cmd := `k3s ctr -n k8s.io images ls 2>/dev/null || k3s ctr images ls 2>/dev/null || ctr -n k8s.io images ls 2>/dev/null || ctr images ls 2>/dev/null || true`
	out, err := commandOutput(ctx, "docker", "exec", node, "sh", "-lc", cmd)
	if err != nil {
		return nil, "", err
	}
	images := make([]string, 0, 32)
	lineIndex := 0
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if lineIndex == 0 && strings.HasPrefix(strings.ToUpper(line), "REF") {
			lineIndex++
			continue
		}
		first := ""
		for field := range strings.FieldsSeq(line) {
			first = field
			break
		}
		if first == "" {
			lineIndex++
			continue
		}
		images = append(images, first)
		lineIndex++
	}
	return images, out, nil
}

func nodeHasImage(images []string, refs, patterns []string) bool {
	set := make(map[string]struct{}, len(images))
	for _, img := range images {
		set[img] = struct{}{}
	}
	for _, r := range refs {
		if _, ok := set[r]; ok {
			return true
		}
	}
	for _, p := range patterns {
		re := regexp.MustCompile(p)
		for _, img := range images {
			if re.MatchString(img) {
				return true
			}
		}
	}
	return false
}

func ensureLocalImages(ctx context.Context, images []string) error {
	for _, image := range images {
		if err := exec.CommandContext(ctx, "docker", "image", "inspect", image).Run(); err == nil {
			continue
		}
		fmt.Printf("Local image not found, pulling: %s\n", image)
		if err := commandRun(ctx, "docker", "pull", image); err != nil {
			return fmt.Errorf("docker pull %s failed: %w", image, err)
		}
	}
	return nil
}

func importImages(ctx context.Context, cluster string, images []string) {
	args := append([]string{"image", "import"}, images...)
	args = append(args, "-c", cluster)
	_ = commandRun(ctx, "k3d", args...)
}

func verifyImagesOnNodes(ctx context.Context, clusterName string, images []string) (bool, error) {
	nodes, err := discoverK3DNodes(ctx, clusterName)
	if err != nil {
		return false, err
	}
	fmt.Printf("Verifying image on nodes: %s\n", strings.Join(nodes, " "))
	missing := false
	for _, image := range images {
		refs := candidateImageRefs(image)
		patterns := candidateImagePatterns(image)
		for _, node := range nodes {
			nodeImages, rawList, err := nodeImageList(ctx, node)
			if err != nil {
				fmt.Fprintf(os.Stderr, "missing image on %s: %s (failed to list node images: %v)\n", node, image, err)
				missing = true
				continue
			}
			if !nodeHasImage(nodeImages, refs, patterns) {
				fmt.Fprintf(os.Stderr, "missing image on %s: %s\n", node, image)
				for line := range strings.SplitSeq(rawList, "\n") {
					line = strings.TrimSpace(line)
					if line == "" {
						continue
					}
					fmt.Fprintf(os.Stderr, "  [node images] %s\n", line)
				}
				missing = true
			}
		}
	}
	return !missing, nil
}

func RunImportImageK3D(ctx context.Context, cfg Config) error {
	clusterRaw, ok := os.LookupEnv("CLUSTER_NAME")
	clusterName := strings.TrimSpace(clusterRaw)
	if !ok || clusterName == "" {
		return fmt.Errorf("CLUSTER_NAME must be set")
	}
	operatorRaw, ok := os.LookupEnv("OPERATOR_IMAGE")
	operatorImage := strings.TrimSpace(operatorRaw)
	if !ok || operatorImage == "" {
		return fmt.Errorf("OPERATOR_IMAGE must be set")
	}

	images := []string{operatorImage}
	if receiverImage := strings.TrimSpace(cfg.AlertReceiverImage); receiverImage != "" {
		images = append(images, receiverImage)
	}
	images = append(images, parseExtraImages(os.Getenv("EXTRA_IMAGES"))...)
	images = uniqueStrings(images)
	if err := ensureLocalImages(ctx, images); err != nil {
		return err
	}

	fmt.Printf("Importing images into cluster %s: %s\n", clusterName, strings.Join(images, " "))
	importImages(ctx, clusterName, images)
	ok, err := verifyImagesOnNodes(ctx, clusterName, images)
	if err != nil {
		return err
	}
	if ok {
		fmt.Println("Image import verification passed")
		return nil
	}

	retries := cfg.ImportRetryCount
	if retries < 0 {
		retries = 0
	}
	for attempt := 1; attempt <= retries; attempt++ {
		fmt.Fprintf(os.Stderr, "Retrying image import (%d/%d)...\n", attempt, retries)
		time.Sleep(2 * time.Second)
		importImages(ctx, clusterName, images)
		ok, err := verifyImagesOnNodes(ctx, clusterName, images)
		if err != nil {
			return err
		}
		if ok {
			fmt.Println("Image import verification passed after retry")
			return nil
		}
	}

	return fmt.Errorf("one or more images are not present on all nodes in cluster %s after retries: %s", clusterName, strings.Join(images, " "))
}
