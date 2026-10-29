package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/voravitl/rein/internal/dagspec"
)

// dagSubcommands maps `rein dag <sub>` to its handler.
var dagSubcommands = map[string]func([]string) int{
	"lint":    cmdDagLint,
	"compile": cmdDagCompile,
}

const dagUsage = `usage:
  rein dag lint --spec FILE [--json]
  rein dag compile --spec FILE --out DIR --config PATH --profile PATH [--apply]`

func cmdDag(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, dagUsage)
		return 2
	}
	h, ok := dagSubcommands[args[0]]
	if !ok {
		fmt.Fprintln(os.Stderr, "unknown dag action:", args[0])
		fmt.Fprintln(os.Stderr, dagUsage)
		return 2
	}
	return h(args[1:])
}

func cmdDagLint(args []string) int {
	fs := flag.NewFlagSet("dag lint", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	spec := fs.String("spec", "", "rein_dag_spec/v1 JSON file")
	asJSON := fs.Bool("json", false, "print the lint result as JSON")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *spec == "" {
		fmt.Fprintln(os.Stderr, dagUsage)
		return 2
	}
	data, err := readDagSpec(*spec)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[dag lint]", err)
		return 2
	}
	_, res := dagspec.Lint(data)
	if *asJSON {
		b, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(b))
	} else {
		for _, f := range res.Errors {
			fmt.Println(formatDagFinding("error", f))
		}
		for _, f := range res.Warnings {
			fmt.Println(formatDagFinding("warning", f))
		}
		if res.Valid {
			fmt.Printf("valid: %d warning(s)\n", len(res.Warnings))
		} else {
			fmt.Printf("refused: %d error(s), %d warning(s)\n", len(res.Errors), len(res.Warnings))
		}
	}
	if !res.Valid {
		return 1
	}
	return 0
}

func readDagSpec(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, dagspec.MaxSpecBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > dagspec.MaxSpecBytes {
		return nil, fmt.Errorf("spec exceeds %d bytes", dagspec.MaxSpecBytes)
	}
	return data, nil
}

func formatDagFinding(level string, f dagspec.Finding) string {
	if f.Node == "" {
		return fmt.Sprintf("%s %s: %s", level, f.Code, f.Message)
	}
	return fmt.Sprintf("%s %s [%s]: %s", level, f.Code, f.Node, f.Message)
}
