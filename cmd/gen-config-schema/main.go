package main

import (
	"flag"
	"fmt"
	"os"

	"ok-gobot/internal/configschema"
)

func main() {
	in := flag.String("in", "docs/ARCHITECTURE-v2.md", "path to architecture-v2 markdown")
	out := flag.String("out", "config.schema.json", "path to generated json schema")
	flag.Parse()

	schema, err := configschema.GenerateSchemaFromFile(*in)
	if err != nil {
		fmt.Fprintf(os.Stderr, "generate schema: %v\n", err)
		os.Exit(1)
	}

	if err := os.WriteFile(*out, schema, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "write schema: %v\n", err)
		os.Exit(1)
	}
}
