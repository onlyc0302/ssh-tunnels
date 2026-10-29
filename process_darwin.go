//go:build darwin

package main

import (
	"bytes"
	"encoding/binary"
	"errors"

	"golang.org/x/sys/unix"
)

func processArgs(pid int) ([]string, error) {
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil, err
	}
	if len(b) < 4 {
		return nil, errors.New("进程参数不可读")
	}
	argc := int(binary.LittleEndian.Uint32(b[:4]))
	b = b[4:]
	i := bytes.IndexByte(b, 0)
	if i < 0 {
		return nil, errors.New("进程参数格式无效")
	}
	b = b[i+1:]
	for len(b) > 0 && b[0] == 0 {
		b = b[1:]
	}
	var args []string
	for len(args) < argc {
		i = bytes.IndexByte(b, 0)
		if i < 0 {
			return nil, errors.New("进程参数不完整")
		}
		args = append(args, string(b[:i]))
		b = b[i+1:]
	}
	return args, nil
}
