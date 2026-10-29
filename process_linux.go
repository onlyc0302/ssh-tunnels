//go:build linux

package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
)

func processArgs(pid int) ([]string, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return nil, errors.New("进程已退出或参数不可读")
	}
	var args []string
	for _, a := range bytes.Split(bytes.TrimSuffix(b, []byte{0}), []byte{0}) {
		args = append(args, string(a))
	}
	return args, nil
}
