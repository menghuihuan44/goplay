// Command goplay-audit performs a static safety audit of the repository.
//
// It scans source code for process-injection and memory-tampering symbols and
// parses native PE import tables with the Go standard library. It never runs,
// maps, or injects into any binary.
//
// Exit codes: 0 = pass, 2 = findings, 1 = execution error.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"goplay/internal/audit"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("goplay-audit", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", ".", "repository root to audit")
	jsonOut := fs.Bool("json", false, "print the JSON report instead of text")
	out := fs.String("out", "", "optional report file path")
	skipPE := fs.Bool("skip-pe", false, "skip native PE import inspection")
	maxBytes := fs.Int64("max-bytes", 8<<20, "maximum source file size to scan")
	timeout := fs.Duration("timeout", 2*time.Minute, "audit timeout")
	var allow, support stringList
	fs.Var(&allow, "allow", "allowlist glob (repeatable; adds to defaults)")
	fs.Var(&support, "support", "support/fixture directory (repeatable; adds to defaults)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	opts := audit.DefaultOptions(*root)
	opts.MaxFileBytes = *maxBytes
	opts.SkipPE = *skipPE
	opts.Timeout = *timeout
	if len(allow) > 0 {
		opts.Allow = append(opts.Allow, allow...)
	}
	if len(support) > 0 {
		opts.SupportDirs = append(opts.SupportDirs, support...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	rep, err := audit.Run(ctx, opts)
	if err != nil {
		fmt.Fprintf(stderr, "goplay-audit: %v\n", err)
		return 1
	}
	var text string
	if *jsonOut {
		b, jerr := rep.JSON()
		if jerr != nil {
			fmt.Fprintf(stderr, "goplay-audit: encode json: %v\n", jerr)
			return 1
		}
		text = string(b) + "\n"
	} else {
		text = rep.Human()
	}
	if *out != "" {
		if *jsonOut {
			err = rep.WriteJSON(*out)
		} else {
			err = rep.WriteText(*out)
		}
		if err != nil {
			fmt.Fprintf(stderr, "goplay-audit: write report: %v\n", err)
			return 1
		}
	}
	fmt.Fprint(stdout, text)
	if rep.Verdict != "PASS" {
		fmt.Fprintf(stderr, "goplay-audit: FAIL: %d source findings, %d critical PE imports\n",
			len(rep.SourceFindings), rep.Stats.PECritical)
		return 2
	}
	fmt.Fprintf(stderr, "goplay-audit: PASS: %d source files, %d native PE files, %d review imports\n",
		rep.Stats.SourceFiles, rep.Stats.NativeFiles, rep.Stats.PEReview)
	return 0
}
