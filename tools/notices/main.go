// notices prints license texts for the Go modules included in this project.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type module struct {
	Path, Version, Dir string
	Main               bool
}

func dependencies(arch string) ([]module, error) {
	cmd := exec.Command("go", "list", "-deps", "-json", ".")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	var modules []module
	dec := json.NewDecoder(pipe)
	for {
		var pkg struct{ Module *module }
		err = dec.Decode(&pkg)
		if err == io.EOF {
			break
		}
		if err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return nil, err
		}
		if pkg.Module != nil && !pkg.Module.Main {
			modules = append(modules, *pkg.Module)
		}
	}
	if err = cmd.Wait(); err != nil {
		return nil, err
	}
	return modules, nil
}

func run() error {
	unique := map[string]module{}
	for _, arch := range []string{"amd64", "arm64"} {
		modules, err := dependencies(arch)
		if err != nil {
			return err
		}
		for _, m := range modules {
			unique[m.Path] = m
		}
	}
	modules := []module{}
	for _, m := range unique {
		modules = append(modules, m)
	}
	sort.Slice(modules, func(i, j int) bool { return modules[i].Path < modules[j].Path })
	fmt.Println("Third-party notices for the Linux amd64 / arm64 binaries")
	goLicense, err := os.ReadFile(filepath.Join(runtime.GOROOT(), "LICENSE"))
	if os.IsNotExist(err) {
		goLicense, err = os.ReadFile(filepath.Join(runtime.GOROOT(), "..", "LICENSE"))
	}
	if err != nil {
		return err
	}
	fmt.Printf("\nGo runtime and standard library\n%s\n", goLicense)
	for _, m := range modules {
		if m.Dir == "" {
			return fmt.Errorf("module %s is not downloaded; run go mod download", m.Path)
		}
		var files []string
		for _, pattern := range []string{"LICENSE*", "COPYING*"} {
			matches, _ := filepath.Glob(filepath.Join(m.Dir, pattern))
			files = append(files, matches...)
		}
		if len(files) == 0 {
			return fmt.Errorf("license file not found for %s", m.Path)
		}
		fmt.Printf("\n============================================================\n%s %s\n", m.Path, m.Version)
		for _, file := range files {
			st, err := os.Stat(file)
			if err != nil {
				return err
			}
			if st.IsDir() {
				continue
			}
			b, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			fmt.Printf("\n--- %s ---\n%s\n", filepath.Base(file), b)
		}
	}
	return nil
}
