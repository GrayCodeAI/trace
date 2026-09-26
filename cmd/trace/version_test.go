package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestVersionCommand(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = writer
	runErr := run([]string{"version"})
	os.Stdout = stdout
	writer.Close()
	out, _ := io.ReadAll(reader)
	if runErr != nil || !strings.HasPrefix(string(out), "trace dev (commit none") {
		t.Fatalf("trace version printed %q (err %v)", out, runErr)
	}
}
