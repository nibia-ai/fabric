package main

import (
	"bytes"
	"flag"
	"fmt"
	"strings"
)

func isHelpArg(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "help", "-h", "--help":
		return true
	default:
		return false
	}
}

func newCommandFlagSet(name, synopsis string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	fs.Usage = func() {
		out := fs.Output()
		fmt.Fprintln(out, "NIBIA Fabric")
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Usage:")
		fmt.Fprintf(out, "  %s\n", synopsis)

		hasFlags := false
		fs.VisitAll(func(*flag.Flag) { hasFlags = true })
		if !hasFlags {
			return
		}

		fmt.Fprintln(out)
		fmt.Fprintln(out, "Options:")
		var b bytes.Buffer
		previous := fs.Output()
		fs.SetOutput(&b)
		fs.PrintDefaults()
		fs.SetOutput(previous)
		text := b.String()
		if strings.HasPrefix(text, "  -") {
			text = "  --" + strings.TrimPrefix(text, "  -")
		}
		text = strings.ReplaceAll(text, "\n  -", "\n  --")
		fmt.Fprint(out, text)
	}
	return fs
}
