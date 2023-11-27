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
	"encoding/json"
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
	"time"
	"unicode"

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
	err             error
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
func (h *hottest) run() error {
	if err := h.canUseGoCommand(); err != nil {
		return errors.New("hottest command requires go command. please install go command")
	}
	return h.runTest()
}

// canUseGoCommand returns true if go command is available.
func (h *hottest) canUseGoCommand() error {
	_, err := exec.LookPath("go")
	return err
}

// runTest runs the test command.
func (h *hottest) runTest() error {
	var wg sync.WaitGroup
	wg.Add(1)
	defer wg.Wait()

	r, w := io.Pipe()
	defer w.Close() //nolint

	args := append([]string{"test"}, h.args...)
	if !slices.Contains(args, "-v") {
		args = append(args, "-v") // This option is required to count the number of tests.
	}
	if !slices.Contains(args, "-json") {
		args = append(args, "-json") // This option is required to parse the test result smoothly.
	}

	cmd := exec.Command("go", args...) //#nosec
	cmd.Stderr = w
	cmd.Stdout = w
	cmd.Env = os.Environ()

	h.interval.Start()
	if err := cmd.Start(); err != nil {
		wg.Done()
		return err
	}

	go h.consume(&wg, r)
	defer func() {
		h.interval.End()
		h.testResult()
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
					h.err = errors.Join(h.err, fmt.Errorf("failed to send signal: %w", err))
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
func (h *hottest) consume(wg *sync.WaitGroup, r io.Reader) {
	defer wg.Done()
	reader := bufio.NewReader(r)
	for {
		l, _, err := reader.ReadLine()
		if err == io.EOF {
			return
		}
		if err != nil {
			h.err = errors.Join(h.err, err)
			return
		}
		h.parse(string(l))
	}
}

// TestOutputJSON represents the structure of a test output log entry.
type TestOutputJSON struct {
	Time    time.Time `json:"Time"`
	Action  string    `json:"Action"`
	Package string    `json:"Package"`
	Test    string    `json:"Test"`
	Output  string    `json:"Output,omitempty"`
	Elapsed float64   `json:"Elapsed,omitempty"`
}

// parse parses a line of test output. It updates the test statistics.
func (h *hottest) parse(line string) {
	var outputJSON TestOutputJSON
	if err := json.Unmarshal([]byte(line), &outputJSON); err != nil {
		h.err = errors.Join(h.err, err)
		return
	}
	trimmed := strings.TrimSpace(outputJSON.Output)

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
		h.allTestMessages = append(h.allTestMessages, strings.TrimRightFunc(outputJSON.Output, unicode.IsSpace))
		return

	// passed
	case strings.HasPrefix(trimmed, "--- PASS"):
		fmt.Fprint(os.Stdout, color.GreenString("."))
		atomic.AddInt32(&h.stats.Pass, 1)
		atomic.StoreInt32(&h.stats.Total, atomic.AddInt32(&h.stats.Total, 1))
		h.allTestMessages = append(h.allTestMessages, strings.TrimRightFunc(outputJSON.Output, unicode.IsSpace))

	// skipped
	case strings.HasPrefix(trimmed, "--- SKIP"):
		fmt.Fprint(os.Stdout, color.BlueString("."))
		atomic.AddInt32(&h.stats.Skip, 1)
		atomic.StoreInt32(&h.stats.Total, atomic.AddInt32(&h.stats.Total, 1))
		h.allTestMessages = append(h.allTestMessages, strings.TrimRightFunc(outputJSON.Output, unicode.IsSpace))

	// failed
	case strings.HasPrefix(trimmed, "--- FAIL"):
		fmt.Fprint(os.Stdout, color.RedString("."))
		atomic.AddInt32(&h.stats.Fail, 1)
		atomic.StoreInt32(&h.stats.Total, atomic.AddInt32(&h.stats.Total, 1))
		h.allTestMessages = append(h.allTestMessages, strings.TrimRightFunc(outputJSON.Output, unicode.IsSpace))

	default:
		h.allTestMessages = append(h.allTestMessages, strings.TrimRightFunc(outputJSON.Output, unicode.IsSpace))
		return
	}
}

// testResult prints the test result.
func (h *hottest) testResult() {
	fmt.Println()

	if h.stats.Fail > 0 {
		fmt.Printf("[Error Messages]\n")
		for _, msg := range extractFailTestMessage(h.allTestMessages) {
			fmt.Printf(" %s\n", strings.TrimRightFunc(msg, unicode.IsSpace))
		}
	}

	fmt.Printf("Results: %s/%s/%s (%s/%s/%s, %s)\n",
		color.GreenString("%d", h.stats.Pass), color.RedString("%d", h.stats.Fail), color.BlueString("%d", h.stats.Skip),
		color.GreenString("%s", "ok"), color.RedString("%s", "ng"), color.BlueString("%s", "skip"),
		h.interval.Duration())

	if h.err != nil {
		fmt.Println()
		fmt.Printf("hottest internal error occurred during test execution: %s\n", h.err.Error())
	}
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
					if isRecordableErrorMessage(v) {
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
			if isRecordableErrorMessage(v) {
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

func isRecordableErrorMessage(s string) bool {
	return !strings.Contains(s, "--- FAIL") &&
		!strings.Contains(s, "--- PASS") &&
		!strings.Contains(s, "--- SKIP") &&
		!strings.Contains(s, "=== RUN") &&
		!strings.Contains(s, "=== CONT") &&
		!strings.Contains(s, "=== PAUSE") &&
		!strings.Contains(s, "=== CONT") &&
		!strings.Contains(s, "=== NAME") &&
		strings.TrimRightFunc(s, unicode.IsSpace) != ""
}
