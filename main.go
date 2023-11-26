// Package main is the entry point for the application.
package main

// Some portions of the code in this file were forked from https://github.com/rakyll/gotest.
// gotest is licensed under the BSD 3-Clause "New" or "Revised" License. The full license text is provided below:
/*
Copyright (c) 2017 The Go Authors. All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

  - Redistributions of source code must retain the above copyright
    notice, this list of conditions and the following disclaimer.
  - Redistributions in binary form must reproduce the above
    copyright notice, this list of conditions and the following disclaimer
    in the documentation and/or other materials provided with the
    distribution.
  - Neither the name of Google Inc. nor the names of its
    contributors may be used to endorse or promote products derived from
    this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
*/

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/fatih/color"
	"github.com/go-spectest/spectest"
	"github.com/nao1215/hottest/version"
	"golang.org/x/exp/slices"
)

var osExit = os.Exit

func main() {
	if err := run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		osExit(1)
	}
	osExit(0)
}

// run execute command.
func run(args []string) error {
	hottest, err := newHottest(args)
	if err != nil {
		if errors.Is(err, errNoArguments) {
			usage()
			return nil // ignore error
		}
	}
	return hottest.run()
}

// usage prints the usage of the hottest command.
func usage() {
	fmt.Printf("hottest %s\n", color.GreenString(version.GetVersion()))
	fmt.Println("User-friendly 'go test' that extracts error messages.")
	fmt.Println("")
	fmt.Println("Usage:")
	fmt.Println("  hottest [arguments]")
	fmt.Println("          ※ The arguments are the same as 'go test'.")
	fmt.Println("Example:")
	fmt.Println("  hottest -cover ./... -coverprofile=cover.out")
}

// TestStats holds the test statistics.
type TestStats struct {
	// Pass is the number of passed tests.
	Pass int32
	// Fail is the number of failed tests.
	Fail int32
	// Skip is the number of skipped tests.
	Skip int32
	// Total is the number of total tests.
	Total int32
}

// hottest is a struct for hottest command.
type hottest struct {
	args            []string
	stats           TestStats
	allTestMessages []string
	interval        *spectest.Interval
}

// errNoArguments is an error that occurs when there are no arguments.
var errNoArguments = errors.New("no arguments")

// newHottest returns a hottest.
func newHottest(args []string) (*hottest, error) {
	if len(args) < 2 {
		return nil, errNoArguments
	}

	return &hottest{
		args:            args[1:],
		stats:           TestStats{},
		allTestMessages: []string{},
		interval:        spectest.NewInterval(),
	}, nil
}

// run runs the hottest command.
func (s *hottest) run() error {
	if err := s.canUseGoCommand(); err != nil {
		return errors.New("hottest command requires go command. please install go command")
	}
	return s.runTest()
}

// canUseGoCommand returns true if go command is available.
func (s *hottest) canUseGoCommand() error {
	_, err := exec.LookPath("go")
	return err
}

// runTest runs the test command.
func (s *hottest) runTest() error {
	var wg sync.WaitGroup
	wg.Add(1)
	defer wg.Wait()

	r, w := io.Pipe()
	defer w.Close() //nolint

	args := append([]string{"test"}, s.args...)
	if !slices.Contains(args, "-v") {
		args = append(args, "-v") // This option is required to count the number of tests.
	}

	cmd := exec.Command("go", args...) //#nosec
	cmd.Stderr = w
	cmd.Stdout = w
	cmd.Env = os.Environ()

	s.interval.Start()
	if err := cmd.Start(); err != nil {
		wg.Done()
		return err
	}

	go s.consume(&wg, r)
	defer func() {
		s.interval.End()
		s.testResult()
	}()

	sigc := make(chan os.Signal, 1)
	done := make(chan struct{})
	defer func() {
		done <- struct{}{}
	}()
	signal.Notify(sigc)

	go func() {
		for {
			select {
			case sig := <-sigc:
				if err := cmd.Process.Signal(sig); err != nil {
					if errors.Is(err, os.ErrProcessDone) {
						break
					}
					fmt.Fprintf(os.Stderr, "failed to send signal: %s", err.Error())
				}
			case <-done:
				return
			}
		}
	}()

	if err := cmd.Wait(); err != nil {
		if _, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok {
			return nil
		}
		return err
	}

	return nil
}

// consume consumes the output of the test command.
func (s *hottest) consume(wg *sync.WaitGroup, r io.Reader) {
	defer wg.Done()
	reader := bufio.NewReader(r)
	for {
		l, _, err := reader.ReadLine()
		if err == io.EOF {
			return
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return
		}
		s.parse(string(l))
	}
}

// parse parses a line of test output. It updates the test statistics.
func (s *hottest) parse(line string) {
	trimmed := strings.TrimSpace(line)

	switch {
	case strings.HasPrefix(trimmed, "ok"):
		fallthrough
	case strings.HasPrefix(trimmed, "FAIL"):
		fallthrough
	case strings.HasPrefix(trimmed, "PASS"):
		fallthrough
	case strings.Contains(trimmed, "[no test files]"):
		return

	case strings.HasPrefix(trimmed, "=== RUN"):
		fallthrough
	case strings.HasPrefix(trimmed, "=== CONT"):
		fallthrough
	case strings.HasPrefix(trimmed, "=== PAUSE"):
		s.allTestMessages = append(s.allTestMessages, line)
		return

	// passed
	case strings.HasPrefix(trimmed, "--- PASS"):
		fmt.Fprint(os.Stdout, color.GreenString("."))
		atomic.AddInt32(&s.stats.Pass, 1)
		atomic.StoreInt32(&s.stats.Total, atomic.AddInt32(&s.stats.Total, 1))
		s.allTestMessages = append(s.allTestMessages, line)

	// skipped
	case strings.HasPrefix(trimmed, "--- SKIP"):
		fmt.Fprint(os.Stdout, color.BlueString("."))
		atomic.AddInt32(&s.stats.Skip, 1)
		atomic.StoreInt32(&s.stats.Total, atomic.AddInt32(&s.stats.Total, 1))
		s.allTestMessages = append(s.allTestMessages, line)

	// failed
	case strings.HasPrefix(trimmed, "--- FAIL"):
		fmt.Fprint(os.Stdout, color.RedString("."))
		atomic.AddInt32(&s.stats.Fail, 1)
		atomic.StoreInt32(&s.stats.Total, atomic.AddInt32(&s.stats.Total, 1))
		s.allTestMessages = append(s.allTestMessages, line)

	default:
		s.allTestMessages = append(s.allTestMessages, line)
		return
	}
}

// testResult prints the test result.
func (s *hottest) testResult() {
	fmt.Println()

	if s.stats.Fail > 0 {
		fmt.Printf("[Error Messages]\n")
		for _, msg := range extractFailTestMessage(s.allTestMessages) {
			fmt.Printf(" %s\n", msg)
		}
	}

	fmt.Printf("Results: %s/%s/%s (%s/%s/%s, %s)\n",
		color.GreenString("%d", s.stats.Pass), color.RedString("%d", s.stats.Fail), color.BlueString("%d", s.stats.Skip),
		color.GreenString("%s", "ok"), color.RedString("%s", "ng"), color.BlueString("%s", "skip"),
		s.interval.Duration())
}

// extractFailTestMessage extracts the error message of the failed test.
func extractFailTestMessage(testResultMsgs []string) []string {
	failTestMessages := []string{}
	beforeRunPos := 0
	lastFailPos := 0
	lastRunMsg := ""

	for i, msg := range testResultMsgs {
		switch {
		case strings.Contains(msg, "=== RUN"):
			if lastRunMsg != "" && strings.Contains(msg, fmt.Sprintf("%s/", lastRunMsg)) {
				continue
			}

			if beforeRunPos < lastFailPos {
				for _, v := range testResultMsgs[beforeRunPos:lastFailPos] {
					if !strings.Contains(v, "--- FAIL") &&
						!strings.Contains(v, "--- PASS") &&
						!strings.Contains(v, "--- SKIP") &&
						!strings.Contains(v, "=== RUN") &&
						!strings.Contains(v, "=== CONT") &&
						!strings.Contains(v, "=== PAUSE") &&
						!strings.Contains(v, "=== CONT") &&
						!strings.Contains(v, "=== NAME") {
						failTestMessages = append(failTestMessages, fmt.Sprintf("    %s", color.RedString(v)))
					}
				}
			}
			lastRunMsg = extractStringBeforeThrash(msg)
			beforeRunPos = i
		case strings.Contains(msg, "--- FAIL"):
			lastFailPos = i
			failTestMessages = append(failTestMessages, msg)
		default:
		}
	}

	if beforeRunPos < lastFailPos {
		for _, v := range testResultMsgs[beforeRunPos:lastFailPos] {
			if !strings.Contains(v, "--- FAIL") &&
				!strings.Contains(v, "--- PASS") &&
				!strings.Contains(v, "--- SKIP") &&
				!strings.Contains(v, "=== RUN") &&
				!strings.Contains(v, "=== CONT") &&
				!strings.Contains(v, "=== PAUSE") &&
				!strings.Contains(v, "=== CONT") &&
				!strings.Contains(v, "=== NAME") {
				failTestMessages = append(failTestMessages, fmt.Sprintf("    %s", color.RedString(v)))
			}
		}
	}
	return failTestMessages
}

// extractStringBeforeThrash extracts the string before the slash.
func extractStringBeforeThrash(s string) string {
	if index := strings.Index(s, "/"); index != -1 {
		return s[:index]
	}
	return s
}
