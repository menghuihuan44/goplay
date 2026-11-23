// Command goplay-eval runs the repeatable offline evaluation suite and writes
// JSON + Markdown reports. Exit code 0 means every threshold passed, 2 means at
// least one threshold failed, and 1 means the run itself could not complete.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"goplay/internal/eval"
)

func main() {
	quick := flag.Bool("quick", false, "run the short evaluation profile")
	asJSON := flag.Bool("json", false, "print the JSON report to stdout")
	reportDir := flag.String("report-dir", "reports", "directory for eval-report.json/.md; empty disables files")
	timeout := flag.Duration("timeout", 5*time.Minute, "overall run timeout")
	flag.Parse()

	ctx := context.Background()
	rep, err := eval.Run(ctx, eval.Options{Quick: *quick, ReportDir: *reportDir, Timeout: *timeout})
	if err != nil {
		fmt.Fprintf(os.Stderr, "goplay-eval: %v\n", err)
		os.Exit(1)
	}
	if *asJSON {
		b, mErr := rep.JSON()
		if mErr != nil {
			fmt.Fprintf(os.Stderr, "goplay-eval: marshal: %v\n", mErr)
			os.Exit(1)
		}
		fmt.Println(string(b))
	} else {
		fmt.Print(rep.Markdown())
	}
	fmt.Fprintf(os.Stderr, "goplay-eval: mode=%s verdict=%s checks=%d/%d failed=%d duration=%dms\n",
		rep.Mode, rep.Summary.Verdict, rep.Summary.ChecksPassed, rep.Summary.ChecksTotal,
		rep.Summary.ChecksFailed, rep.DurationMS)
	if rep.JSONPath != "" {
		fmt.Fprintf(os.Stderr, "goplay-eval: wrote %s and %s\n", rep.JSONPath, rep.MarkdownPath)
	}
	if rep.Summary.Verdict != "PASS" {
		os.Exit(2)
	}
}
