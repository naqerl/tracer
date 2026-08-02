package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
)

// commandHelp describes one CLI subcommand for -help output.
type commandHelp struct {
	name    string
	args    string // positional arguments, e.g. "<source>"; empty if none
	summary string
	flags   *flag.FlagSet // nil if the command takes no flags
}

// printUsage renders the complete CLI interface: global flags, every
// subcommand with its flags, and the daemon invocation. Explicit help
// (`tracer-collector help`, `-help` piped) writes to stdout; the flag
// package also calls the installed flag.Usage on parse errors, in which case
// the caller passes the FlagSet's output stream (stderr).
func printUsage(w io.Writer, global *flag.FlagSet, commands []commandHelp) {
	fmt.Fprintln(w, "tracer-collector — Tracer log collector")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  tracer-collector [flags] <command> [args]")
	fmt.Fprintln(w, "  tracer-collector                 start the collector daemon (default)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Global flags:")
	fmt.Fprint(w, flagDefaults(global))
	for _, c := range commands {
		fmt.Fprintln(w)
		if c.args != "" {
			fmt.Fprintf(w, "  %s %s\n", c.name, c.args)
		} else {
			fmt.Fprintf(w, "  %s\n", c.name)
		}
		fmt.Fprintf(w, "        %s\n", c.summary)
		if c.flags != nil {
			fmt.Fprint(w, flagDefaults(c.flags))
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, `Run "tracer-collector <command> -help" for a command's usage.`)
}

// flagDefaults renders a FlagSet's flags to a string, leaving the FlagSet's
// output (used for parse errors) untouched.
func flagDefaults(fs *flag.FlagSet) string {
	var buf bytes.Buffer
	out := fs.Output()
	fs.SetOutput(&buf)
	fs.PrintDefaults()
	fs.SetOutput(out)
	return buf.String()
}

// setCommandUsage installs a Usage function on a subcommand FlagSet that
// prints a usage line plus the command's flags, so
// `tracer-collector <command> -help` is complete and consistent. The flag
// package calls Usage both for an explicit -help and for parse errors; both
// go to the FlagSet's output stream (stderr by default), matching Go's flag
// conventions.
func setCommandUsage(fs *flag.FlagSet, usageLine string) {
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: %s\n", usageLine)
		if n := flagCount(fs); n > 0 {
			fmt.Fprintln(fs.Output())
			fmt.Fprintln(fs.Output(), "Flags:")
			fmt.Fprint(fs.Output(), flagDefaults(fs))
		}
	}
}

// flagCount returns how many flags are registered on fs.
func flagCount(fs *flag.FlagSet) int {
	n := 0
	fs.VisitAll(func(*flag.Flag) { n++ })
	return n
}

// printCommandUsage prints the usage of one command to os.Stdout, used by
// the `help <command>` alias.
func printCommandUsage(c commandHelp) {
	if c.flags != nil {
		c.flags.Usage()
		return
	}
	if c.args != "" {
		fmt.Fprintf(os.Stdout, "Usage: tracer-collector %s %s\n", c.name, c.args)
	} else {
		fmt.Fprintf(os.Stdout, "Usage: tracer-collector %s\n", c.name)
	}
	fmt.Fprintf(os.Stdout, "\n%s\n", c.summary)
}
