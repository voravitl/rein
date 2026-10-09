package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/voravitl/rein/internal/e2e"
)

func cmdE2E(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: rein e2e <run|test|check> [args]")
		return 2
	}

	switch args[0] {
	case "check":
		return cmdE2ECheck(args[1:])
	case "run":
		return cmdE2ERun(args[1:])
	case "test":
		return cmdE2ETest(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "rein e2e: unknown subcommand %q\n", args[0])
		return 2
	}
}

func cmdE2ECheck(args []string) int {
	fs := flag.NewFlagSet("e2e check", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit json output")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	client := e2e.NewRealOrcaClient()
	err := client.CheckStatus()

	if *asJSON {
		res := map[string]any{
			"ok": err == nil,
		}
		if err != nil {
			res["error"] = err.Error()
		} else {
			res["message"] = "Orca browser runtime is ready and reachable"
		}
		data, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(data))
	} else {
		if err != nil {
			fmt.Fprintf(os.Stderr, "rein e2e check: failed: %v\n", err)
			return 1
		}
		fmt.Println("rein e2e check: Orca browser runtime is ready and reachable")
	}

	if err != nil {
		return 1
	}
	return 0
}

func cmdE2ETest(args []string) int {
	fs := flag.NewFlagSet("e2e test", flag.ContinueOnError)
	outDir := fs.String("out", "", "evidence output directory (default: ./report/e2e)")
	timeoutSec := fs.Int("timeout", 30, "timeout in seconds")
	asJSON := fs.Bool("json", false, "emit json test report")
	if err := fs.Parse(reorderFlagsFirst(args)); err != nil {
		return 2
	}

	posArgs := fs.Args()
	if len(posArgs) < 1 {
		fmt.Fprintln(os.Stderr, "usage: rein e2e test <url-or-spec> [--out DIR] [--timeout SEC] [--json]")
		return 2
	}

	target := posArgs[0]
	var spec *e2e.Spec
	var err error

	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		// Generate an automated smoke test spec
		spec = &e2e.Spec{
			Name:           "smoke-browser-test",
			Description:    fmt.Sprintf("Automated smoke verification for %s", target),
			BaseURL:        target,
			TimeoutSeconds: *timeoutSec,
			Steps: []e2e.Step{
				{Action: "goto", URL: target, Description: "Navigate to target URL"},
				{Action: "snapshot", Description: "Capture initial accessibility state"},
				{Action: "screenshot", Filename: "smoke-verified.png", Description: "Capture visual verification"},
			},
		}
	} else {
		spec, err = e2e.LoadSpec(target)
		if err != nil {
			fmt.Fprintf(os.Stderr, "rein e2e test error: %v\n", err)
			return 2
		}
		if *timeoutSec > 0 {
			spec.TimeoutSeconds = *timeoutSec
		}
	}

	evidenceDir := *outDir
	if evidenceDir == "" {
		if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
			evidenceDir = filepath.Join(filepath.Dir(target), "evidence")
		} else {
			evidenceDir = filepath.Join("report", "e2e")
		}
	}

	return runSpec(spec, evidenceDir, *asJSON)
}

func cmdE2ERun(args []string) int {
	fs := flag.NewFlagSet("e2e run", flag.ContinueOnError)
	outDir := fs.String("out", "", "evidence output directory (default: <spec-dir>/evidence)")
	baseURL := fs.String("base-url", "", "override base URL")
	timeoutSec := fs.Int("timeout", 0, "override timeout in seconds")
	asJSON := fs.Bool("json", false, "emit json test report")
	if err := fs.Parse(reorderFlagsFirst(args)); err != nil {
		return 2
	}

	posArgs := fs.Args()
	if len(posArgs) < 1 {
		fmt.Fprintln(os.Stderr, "usage: rein e2e run <spec-file> [--out DIR] [--base-url URL] [--timeout SEC] [--json]")
		return 2
	}

	specPath := posArgs[0]
	spec, err := e2e.LoadSpec(specPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rein e2e run error: %v\n", err)
		return 2
	}

	if *baseURL != "" {
		spec.BaseURL = *baseURL
	}
	if *timeoutSec > 0 {
		spec.TimeoutSeconds = *timeoutSec
	}

	evidenceDir := *outDir
	if evidenceDir == "" {
		evidenceDir = filepath.Join(filepath.Dir(specPath), "evidence")
	}

	return runSpec(spec, evidenceDir, *asJSON)
}

func runSpec(spec *e2e.Spec, evidenceDir string, asJSON bool) int {
	client := e2e.NewRealOrcaClient()
	if err := client.CheckStatus(); err != nil {
		fmt.Fprintf(os.Stderr, "rein e2e error: Orca runtime not ready: %v\n", err)
		return 2
	}

	runner := e2e.NewRunner(client, evidenceDir)
	report, err := runner.Run(spec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rein e2e failed to execute: %v\n", err)
		return 1
	}

	if asJSON {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Println(string(data))
	} else {
		fmt.Printf("=== E2E Test Run: %s ===\n", report.SpecName)
		fmt.Printf("Status: %d/%d steps passed (%d ms)\n", report.PassedSteps, report.TotalSteps, report.DurationMS)
		if report.EvidenceDir != "" {
			fmt.Printf("Evidence directory: %s\n", report.EvidenceDir)
		}
		if report.PDFPath != "" {
			fmt.Printf("Board PDF report:   %s\n", report.PDFPath)
		}
		if !report.Success {
			fmt.Fprintf(os.Stderr, "FAILURE: %s\n", report.FailureMessage)
			return 1
		}
		fmt.Println("SUCCESS: All steps passed.")
	}

	if !report.Success {
		return 1
	}
	return 0
}

func reorderFlagsFirst(args []string) []string {
	var flags []string
	var posArgs []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			flags = append(flags, arg)
			if !strings.Contains(arg, "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				flags = append(flags, args[i])
			}
		} else {
			posArgs = append(posArgs, arg)
		}
	}
	return append(flags, posArgs...)
}
