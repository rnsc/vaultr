package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strings"

	"github.com/rnsc/vaultr/internal/vault"
)

// vaultr env / vaultr exec hand a secret's keys to other programs as
// environment variables. Values go to stdout (env) or to the child's
// environment (exec) and are never written to disk.

type envVar struct {
	name, value string
}

// exitError carries a child process's exit code through run() to main.
type exitError struct{ code int }

func (e exitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }

// envName turns a key name into an environment variable name: upper case,
// with anything but letters, digits and "_" replaced by "_", and a leading
// "_" when it would start with a digit.
func envName(prefix, key string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(prefix + key) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	s := b.String()
	if s == "" || (s[0] >= '0' && s[0] <= '9') {
		s = "_" + s
	}
	return s
}

// secretEnv reads the secrets at paths and returns their keys as variables,
// sorted by name. When two keys map to the same name, the later path wins
// and a warning says so.
func (a *app) secretEnv(ctx context.Context, paths []string, prefix string) ([]envVar, error) {
	byName := map[string]string{}
	from := map[string]string{}
	for _, p := range paths {
		p = strings.Trim(p, "/")
		m, err := a.client.MountFor(ctx, p)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p, strings.TrimSuffix(m.Path, "/")), "/")
		data, err := a.client.ReadSecret(ctx, m, rel)
		if err != nil {
			return nil, err
		}
		for k, v := range vault.Stringify(data) {
			n := envName(prefix, k)
			if prev, ok := from[n]; ok {
				fmt.Fprintf(os.Stderr, "warning: %s from %s replaces the one from %s\n", n, p, prev)
			}
			byName[n], from[n] = v, p
		}
	}
	out := make([]envVar, 0, len(byName))
	for n, v := range byName {
		out = append(out, envVar{n, v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

// shQuote quotes s for POSIX shells.
func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// fishQuote quotes s for fish, where only \ and ' are special in '...'.
func fishQuote(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

var envFormats = []string{"sh", "fish", "json"}

func (a *app) env(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("env", flag.ContinueOnError)
	prefix := fs.String("prefix", "", "prefix for the variable names, e.g. APP_")
	format := fs.String("format", "sh", "output: sh (export NAME='value'), fish or json")
	paths, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return errors.New("usage: vaultr env [--prefix P] [--format sh|fish|json] PATH [PATH ...]")
	}
	if !slices.Contains(envFormats, *format) {
		return fmt.Errorf("env: unknown format %q (%s)", *format, strings.Join(envFormats, ", "))
	}
	vars, err := a.secretEnv(ctx, paths, *prefix)
	if err != nil {
		return err
	}
	switch *format {
	case "json":
		obj := make(map[string]string, len(vars))
		for _, v := range vars {
			obj[v.name] = v.value
		}
		return json.NewEncoder(os.Stdout).Encode(obj)
	case "fish":
		for _, v := range vars {
			fmt.Printf("set -gx %s %s\n", v.name, fishQuote(v.value))
		}
	default:
		for _, v := range vars {
			fmt.Printf("export %s=%s\n", v.name, shQuote(v.value))
		}
	}
	return nil
}

func (a *app) exec(ctx context.Context, args []string) error {
	i := slices.Index(args, "--")
	if i < 0 || i == len(args)-1 {
		return errors.New("usage: vaultr exec [--prefix P] PATH... -- COMMAND [ARGS...]")
	}
	fs := flag.NewFlagSet("exec", flag.ContinueOnError)
	prefix := fs.String("prefix", "", "prefix for the variable names, e.g. APP_")
	paths, err := parseInterspersed(fs, args[:i])
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return errors.New("exec: no secret path before --")
	}
	vars, err := a.secretEnv(ctx, paths, *prefix)
	if err != nil {
		return err
	}
	cmd := exec.Command(args[i+1], args[i+2:]...)
	cmd.Env = os.Environ()
	for _, v := range vars {
		cmd.Env = append(cmd.Env, v.name+"="+v.value)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	// ^c reaches the command through the terminal; vaultr itself ignores
	// it (see main) and waits, then exits with the command's status.
	err = cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return exitError{ee.ExitCode()}
	}
	return err
}
