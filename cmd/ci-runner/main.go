package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/pbsladek/capacity-planning-operator/internal/cirunner"
)

func usage() {
	fmt.Fprintf(os.Stderr, "Usage:\n")
	fmt.Fprintf(os.Stderr, "  ci-runner integration\n")
	fmt.Fprintf(os.Stderr, "  ci-runner collect-diagnostics\n")
	fmt.Fprintf(os.Stderr, "  ci-runner import-image-k3d\n")
	fmt.Fprintf(os.Stderr, "  ci-runner nightly-alert-delivery\n")
}

func runIntegration(args []string) error {
	fs := flag.NewFlagSet("integration", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg := cirunner.LoadConfig()
	runner, err := cirunner.NewIntegrationRunner(cfg)
	if err != nil {
		return err
	}
	return runner.Run(context.Background())
}

func runCollectDiagnostics(args []string) error {
	fs := flag.NewFlagSet("collect-diagnostics", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg := cirunner.LoadConfig()
	runner, err := cirunner.NewDiagnosticsRunner(cfg)
	if err != nil {
		return err
	}
	return runner.Run(context.Background())
}

func runImportImageK3D(args []string) error {
	fs := flag.NewFlagSet("import-image-k3d", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg := cirunner.LoadConfig()
	return cirunner.RunImportImageK3D(context.Background(), cfg)
}

func runNightlyAlertDelivery(args []string) error {
	fs := flag.NewFlagSet("nightly-alert-delivery", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg := cirunner.LoadConfig()
	runner, err := cirunner.NewNightlyAlertDeliveryRunner(cfg)
	if err != nil {
		return err
	}
	return runner.Run(context.Background())
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "integration":
		err = runIntegration(os.Args[2:])
	case "collect-diagnostics":
		err = runCollectDiagnostics(os.Args[2:])
	case "import-image-k3d":
		err = runImportImageK3D(os.Args[2:])
	case "nightly-alert-delivery":
		err = runNightlyAlertDelivery(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		usage()
		err = fmt.Errorf("unknown command: %s", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
}
