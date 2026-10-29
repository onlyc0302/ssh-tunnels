package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mattn/go-shellwords"
)

type parsedSSH struct {
	Destination string
	Args        []string
	Forwards    []Forward
	PureTunnel  bool
	Unsupported []string
}

// Bracketed IPv6 must remain a single field.
func parseForward(spec string) (Forward, error) {
	var fields []string
	start, depth := 0, 0
	for i, c := range spec {
		switch c {
		case '[':
			depth++
		case ']':
			depth--
		case ':':
			if depth == 0 {
				fields = append(fields, spec[start:i])
				start = i + 1
			}
		}
		if depth < 0 || depth > 1 {
			return Forward{}, errors.New("IPv6 方括号不匹配")
		}
	}
	if depth != 0 {
		return Forward{}, errors.New("IPv6 方括号不匹配")
	}
	fields = append(fields, spec[start:])
	if len(fields) == 3 {
		fields = append([]string{"127.0.0.1"}, fields...)
	}
	if len(fields) != 4 {
		return Forward{}, errors.New("仅支持 TCP -L [监听地址:]本地端口:目标主机:目标端口")
	}
	if fields[0] == "" {
		fields[0] = "*"
	}
	p, e := strconv.Atoi(fields[1])
	if e != nil {
		return Forward{}, errors.New("本地端口无效")
	}
	tp, e := strconv.Atoi(fields[3])
	if e != nil {
		return Forward{}, errors.New("目标端口无效")
	}
	f := Forward{strings.Trim(fields[0], "[]"), p, strings.Trim(fields[2], "[]"), tp}
	return f, f.Validate()
}

var safeOptions = map[string]bool{
	"hostname": true, "user": true, "port": true, "identityfile": true, "identityagent": true,
	"identitiesonly": true, "proxyjump": true, "hostkeyalias": true, "userknownhostsfile": true,
	"addressfamily": true, "preferredauthentications": true, "pubkeyauthentication": true,
}

func safeOption(v string) bool {
	fields := strings.FieldsFunc(v, func(r rune) bool { return r == '=' || r == ' ' || r == '\t' })
	if len(fields) < 2 {
		return false
	}
	return safeOptions[strings.ToLower(fields[0])]
}

func parseSSH(argv []string) (parsedSSH, error) {
	var p parsedSSH
	if len(argv) == 0 || filepath.Base(argv[0]) != "ssh" {
		return p, errors.New("请输入以 ssh 开头的命令")
	}
	remote := false
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		if remote {
			p.Unsupported = append(p.Unsupported, "远端命令")
			continue
		}
		if a == "--" {
			if i+1 < len(argv) && p.Destination == "" {
				i++
				p.Destination = argv[i]
			}
			remote = true
			continue
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			if p.Destination == "" {
				p.Destination = a
			} else {
				p.Unsupported = append(p.Unsupported, "远端命令")
				remote = true
			}
			continue
		}
		for j := 1; j < len(a); j++ {
			flag := a[j]
			if strings.ContainsRune("BbcDEeFIiJLlmOopRSWw", rune(flag)) {
				v := a[j+1:]
				if v == "" {
					i++
					if i >= len(argv) {
						return p, fmt.Errorf("-%c 缺少参数", flag)
					}
					v = argv[i]
				}
				switch flag {
				case 'L':
					f, e := parseForward(v)
					if e != nil {
						p.Unsupported = append(p.Unsupported, e.Error())
					} else {
						p.Forwards = append(p.Forwards, f)
					}
				case 'p', 'i', 'J', 'l', 'F':
					p.Args = append(p.Args, "-"+string(flag), v)
				case 'o':
					if strings.TrimSpace(v) == "" {
						return p, errors.New("空 -o 参数")
					}
					if safeOption(v) {
						p.Args = append(p.Args, "-o", v)
					} else {
						p.Unsupported = append(p.Unsupported, "需人工检查的 -o "+strings.SplitN(v, "=", 2)[0])
					}
				default:
					p.Unsupported = append(p.Unsupported, fmt.Sprintf("-%c", flag))
				}
				break
			}
			switch flag {
			case 'N':
				p.PureTunnel = true
			case 'q', 'T', 'f', 'n', 'v':
			case '4', '6', 'C':
				p.Args = append(p.Args, "-"+string(flag))
			default:
				p.Unsupported = append(p.Unsupported, fmt.Sprintf("-%c", flag))
			}
		}
	}
	if p.Destination == "" {
		return p, errors.New("缺少 SSH 主机")
	}
	if strings.HasPrefix(p.Destination, "-") {
		return p, errors.New("SSH 主机不能以 - 开头")
	}
	return p, nil
}

func importCommand(command, name, group string) (Definition, error) {
	parser := shellwords.NewParser()
	parser.ParseEnv = false
	parser.ParseBacktick = false
	if strings.ContainsAny(command, "\n\r") {
		return Definition{}, errors.New("请粘贴单行 SSH 命令")
	}
	argv, err := parser.Parse(command)
	if err != nil {
		return Definition{}, err
	}
	// Shell control operators are never evaluated or silently discarded.
	if parser.Position != -1 {
		return Definition{}, errors.New("仅支持单条 ssh 命令，不支持管道或 shell 操作符")
	}
	p, err := parseSSH(argv)
	if err != nil {
		return Definition{}, err
	}
	if len(p.Unsupported) > 0 {
		return Definition{}, fmt.Errorf("暂不支持自动导入: %s", strings.Join(p.Unsupported, ", "))
	}
	if name == "" {
		name = p.Destination
	}
	d := Definition{ID: newID(), Name: name, Group: group, Destination: p.Destination, Args: p.Args, Forwards: p.Forwards, AutoReconnect: true}
	return d, d.Validate()
}

func validateTransportArgs(args []string) error {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-4", "-6", "-C":
		case "-p", "-i", "-J", "-l", "-F", "-o":
			flag := args[i]
			i++
			if i == len(args) || strings.TrimSpace(args[i]) == "" {
				return errors.New("SSH 参数缺少值")
			}
			if flag == "-o" && !safeOption(args[i]) {
				return errors.New("配置包含不支持的 SSH 选项")
			}
		default:
			return fmt.Errorf("不支持的 SSH 参数 %q", args[i])
		}
	}
	return nil
}
