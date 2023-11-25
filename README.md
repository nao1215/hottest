[![LinuxUnitTest](https://github.com/nao1215/hottest/actions/workflows/linux_test.yml/badge.svg)](https://github.com/nao1215/hottest/actions/workflows/linux_test.yml)
[![MacUnitTest](https://github.com/nao1215/hottest/actions/workflows/mac_test.yml/badge.svg)](https://github.com/nao1215/hottest/actions/workflows/mac_test.yml)
[![WindowsUnitTest](https://github.com/nao1215/hottest/actions/workflows/windows_test.yml/badge.svg)](https://github.com/nao1215/hottest/actions/workflows/windows_test.yml)
[![reviewdog](https://github.com/nao1215/hottest/actions/workflows/reviewdog.yml/badge.svg)](https://github.com/nao1215/hottest/actions/workflows/reviewdog.yml)
## What is hottest ?
The hottest part in unit testing is the **error messages**.
  
The `hottest` command extracts error messages from the unit test logs, saving the effort of searching for error messages.  It's usage is the same as the `go test` command. 
![example](./doc/image/demo.gif)

The `hottest` command is the wrapper for 'go test'. It adds the "-v" option to the 'go test' options provided by the user and executes the tests. Successful test results are represented by green ".", while failed tests are represented by red ".".
  
Upon completion of the tests, it displays information about the failed tests and summarizes the test results.


## Installation
```bash
go install github.com/nao1215/hottest@latest
```

## Usage
```bash
Usage:
  hottest [arguments]
          ※ The arguments are the same as 'go test'.
Example:
  hottest -cover ./... -coverprofile=cover.out
```

Example:
```bash
$ hottest ./...
hottest v0.0.1 execute 'go test'

...............................................................
[Error Messages]
 --- FAIL: TestPlainText (0.00s)
     --- FAIL: TestPlainText/success_PlainText() (0.00s)
         markdown_test.go:25: value is mismatch (-want +got):
               []string{
             -  "Hllo",
             +  "Hello",
               }

[Test Results]
 - Execution Time: 242.172244ms
 - Total         : 63
 - Passed        : 61
 - Failed        : 2
 - Skipped       : 0
```

## LICENSE
[BSD 3-Clause License](./LICENSE)
  
Some portions of the code in this file were forked from [rakyll/gotest](https://github.com/rakyll/gotest). The `gotest` is licensed under the BSD 3-Clause "New" or "Revised" License. Full license text is available in [main.go](./main.go)

## Origin of the Name
The `hottest` is a command developed with inspiration from [rakyll/gotest](https://github.com/rakyll/gotest). While `gotest` adds color to error logs, as the volume of unit tests increases, it becomes challenging to locate error messages with color alone.

To solve this issue, the idea emerged to make a slight improvement to `gotest`, leading to the development of `hottest`. Advancing just one step from 'g' takes you to 'h'.   
  
I liked "hotest," but to avoid being corrected for a spelling mistake, I chose "hottest."
