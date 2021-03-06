// Public Domain (-) 2010-2011 The Golly Authors.
// See the Golly UNLICENSE file for details.

// Package command provides utility functions for executing system commands.
package command

import (
	"bytes"
	"fmt"
	"io"
	"os"
)

type CommandError struct {
	Command string
	Args    []string
}

func (err *CommandError) Error() string {
	return fmt.Sprintf("Couldn't successfully execute: %s %v", err.Command, err.Args)
}

// GetOutput returns the output from running the given command arguments.
func GetOutput(args []string) (output string, error error) {
	var (
		buffer  *bytes.Buffer
		process *os.Process
	)
	read_pipe, write_pipe, err := os.Pipe()
	if err != nil {
		goto Error
	}
	defer read_pipe.Close()
	process, err = os.StartProcess(args[0], args,
		&os.ProcAttr{
			Dir:   ".",
			Env:   os.Environ(),
			Files: []*os.File{nil, write_pipe, nil},
		})
	if err != nil {
		write_pipe.Close()
		goto Error
	}
	_, err = process.Wait()
	write_pipe.Close()
	if err != nil {
		goto Error
	}
	buffer = &bytes.Buffer{}
	_, err = io.Copy(buffer, read_pipe)
	if err != nil {
		goto Error
	}
	output = buffer.String()
	return output, nil
Error:
	return "", &CommandError{args[0], args}
}
