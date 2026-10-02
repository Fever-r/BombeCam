// Command setup stores the Osaio values that BombeCam builds include (the
// server key and the app ID), so neither has to be in the source code. Run it
// from the repository root with Go installed:
//
//	go run ./tools/setup                         ask for both and save osaio-setup.txt
//	go run ./tools/setup -key KEY -app-id ID     save them without asking
//	go run ./tools/setup -if-missing             ask only when one is not set up (build.cmd)
//	go run ./tools/setup -ldflags                print the linker flags that build them in
//	                                             (nothing when none is set up; make,
//	                                             build.ps1 and the Dockerfile use this)
//
// BOMBECAM_SERVER_KEY and BOMBECAM_APP_ID, when set, are used instead of the
// file. A build without one of them asks for it on its web page.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Fever-r/BombeCam/internal/buildkey"
)

func main() {
	key := flag.String("key", "", "save this Osaio server key without asking")
	appID := flag.String("app-id", "", "save this Osaio app ID without asking")
	ifMissing := flag.Bool("if-missing", false, "only ask when a value is not set up")
	ldflags := flag.Bool("ldflags", false, "print the -ldflags argument that builds the values in")
	flag.Parse()
	given := map[string]string{buildkey.ServerKey.Name: *key, buildkey.AppID.Name: *appID}
	if err := run(os.Stdin, os.Stdout, os.Stderr, given, *ifMissing, *ldflags); err != nil {
		fmt.Fprintln(os.Stderr, "setup:", err)
		os.Exit(1)
	}
}

// missingNote says what a build without v does.
func missingNote(buildkey.Value) string {
	return "this build will ask for it on its web page"
}

func run(in io.Reader, out, msg io.Writer, given map[string]string, ifMissing, ldflags bool) error {
	if _, err := os.Stat("go.mod"); err != nil {
		return errors.New("run this from the top folder of the BombeCam source")
	}
	found, err := buildkey.Find(".", nil)
	if ldflags {
		// Only the flags go to out: a build reads them. Notes go to msg.
		if err != nil {
			return err
		}
		for _, v := range found.Missing() {
			fmt.Fprintf(msg, "setup: no %s set up; %s (run: go run ./tools/setup)\n", v.Label, missingNote(v))
		}
		if flags := buildkey.LDFlags(found); flags != "" {
			fmt.Fprintln(out, flags)
		}
		return nil
	}
	var se *buildkey.SourceError
	if errors.As(err, &se) && se.From == se.Value.Env {
		return fmt.Errorf("%v; fix or clear %s first", err, se.Value.Env)
	}
	broken := err != nil // osaio-setup.txt is there but has a problem

	// What the file holds now. From a broken file the values that still read
	// fine are kept, so fixing one line never loses the other value.
	saved, _ := buildkey.ReadFile(buildkey.File)

	if given[buildkey.ServerKey.Name] != "" || given[buildkey.AppID.Name] != "" {
		for name, val := range given {
			if val != "" {
				saved[name] = val
			}
		}
		return save(out, saved)
	}
	if ifMissing && !broken && len(found.Missing()) == 0 {
		fmt.Fprintf(out, "Builds include the Osaio server key and app ID (%s).\n", describe(found))
		return nil
	}

	fmt.Fprint(out, `BombeCam signs its requests to Osaio's servers with two values from the Osaio
app: a server key and an app ID. Release downloads have both built in; a build
from source takes them from `+buildkey.File+` (git ignores that file), obscured in the
binary.

`)
	if broken {
		fmt.Fprintf(out, "%s can't be used: %v\nEnter working values to replace it, or delete the file to build without them.\n\n", buildkey.File, err)
	}
	reader := bufio.NewReader(in)
	changed := false
	for _, v := range buildkey.Values {
		have := ""
		switch {
		case found.From[v.Name] == v.Env:
			fmt.Fprintf(out, "The %s comes from %s, which builds use whatever you save here.\n", v.Label, v.Env)
		case saved[v.Name] != "":
			have = " (Enter keeps the one saved)"
		default:
			have = " (Enter skips: " + missingNote(v) + ")"
		}
		fmt.Fprintf(out, "Paste the %s and press Enter%s: ", v.Label, have)
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		fmt.Fprintln(out)
		if val := strings.TrimSpace(line); val != "" {
			if _, err := buildkey.Check(v, val); err != nil {
				return err
			}
			saved[v.Name] = val
			changed = true
		}
	}
	if !changed {
		if broken {
			return fmt.Errorf("%s can't be used: enter working values, or delete the file to build without them", buildkey.File)
		}
		fmt.Fprintln(out, "Nothing changed.")
		return nil
	}
	return save(out, saved)
}

func save(out io.Writer, values map[string]string) error {
	if err := buildkey.Write(buildkey.File, values); err != nil {
		return err
	}
	fmt.Fprintf(out, "Saved to %s.\n", buildkey.File)
	found, err := buildkey.Find(".", nil)
	if err != nil {
		return err
	}
	for _, v := range buildkey.Values {
		switch {
		case found.From[v.Name] == v.Env:
			fmt.Fprintf(out, "Note: %s is set, so builds use it for the %s until it is cleared.\n", v.Env, v.Label)
		case found.Value[v.Name] == "":
			fmt.Fprintf(out, "No %s yet: %s.\n", v.Label, missingNote(v))
		}
	}
	return nil
}

// describe names where the values come from, for -if-missing.
func describe(f buildkey.Found) string {
	var parts []string
	for _, v := range buildkey.Values {
		from := f.From[v.Name]
		if from != v.Env {
			from = filepath.Base(from)
		}
		parts = append(parts, v.Label+" from "+from)
	}
	return strings.Join(parts, ", ")
}
