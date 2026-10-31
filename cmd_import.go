package main

import (
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/KooshaPari/dagrun/internal/tracera"
)

// cmdImport reconstructs a plan from a Tracera payload (file or stdin) and
// writes it as YAML to --out (or stdout). Dropped items are reported on stderr.
func cmdImport(args []string) error {
	var dir, in, out string
	var jsonOut bool
	fs := flagSet("import", &dir, &jsonOut)
	fs.StringVar(&in, "in", "-", "input file (Tracera payload JSON); - for stdin")
	fs.StringVar(&out, "out", "", "output file; empty writes to stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var data []byte
	var err error
	if in == "-" {
		data, err = readAllStdin()
	} else {
		data, err = os.ReadFile(in)
	}
	if err != nil {
		return fmt.Errorf("read input: %w", err)
	}

	res, err := tracera.ImportJSON(data)
	if err != nil {
		// Still print whatever was reconstructed, to aid debugging.
		if res.Plan != nil {
			for _, d := range res.Dropped {
				fmt.Fprintf(os.Stderr, "dropped: %s\n", d)
			}
		}
		return err
	}

	for _, d := range res.Dropped {
		fmt.Fprintf(os.Stderr, "dropped: %s\n", d)
	}

	p := res.Plan
	// Render as plan YAML. plan.Plan has a MarshalYAML via yaml? Use plan.Save helper if exists.
	raw, err := yaml.Marshal(p)
	if err != nil {
		return err
	}

	if out == "" {
		_, err = os.Stdout.Write(append(raw, '\n'))
		return err
	}
	return os.WriteFile(out, append(raw, '\n'), 0o644)
}

func readAllStdin() ([]byte, error) {
	return io.ReadAll(os.Stdin)
}
