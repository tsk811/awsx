package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
)

const usage = `Usage: awsx <command>

  init            Configure IAM Identity Center
  login           Select an account and role, then export credentials
  region          Select an AWS service region
`

const zshIntegration = `awsx() {
  case "${1-}" in
    init|login|region)
      local awsx_output
      awsx_output=$(command awsx "$@") || return $?
      eval "$awsx_output"
      ;;
    *) command awsx "$@" ;;
  esac
}
`

var errCancelled = errors.New("cancelled")

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "awsx: "+terminalText(err.Error()))
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 2 && args[0] == "shell-init" && args[1] == "zsh" {
		_, err := fmt.Fprint(os.Stdout, zshIntegration)
		return err
	}
	if len(args) == 0 || (len(args) == 1 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help")) {
		_, err := fmt.Fprint(os.Stdout, usage)
		return err
	}
	if len(args) != 1 || (args[0] != "init" && args[0] != "login" && args[0] != "region") {
		return errors.New("unsupported command or arguments; use awsx help")
	}
	var path string
	var c config
	var err error
	if args[0] != "region" {
		path, err = configPath()
		if err != nil {
			return err
		}
		if args[0] == "login" {
			c, err = loadConfig(path)
			if err != nil {
				return err
			}
		}
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open interactive terminal: %w", err)
	}
	defer tty.Close()
	configureStyles(tty)
	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	// Cancelling AWS work must not kill the renderer before its final blank frame.
	ctx, cancel := context.WithCancel(signalCtx)
	defer cancel()
	m := newModel(ctx, cancel, args[0], path, c)
	p := tea.NewProgram(m, tea.WithInput(tty), tea.WithOutput(tty), tea.WithContext(signalCtx), tea.WithoutSignalHandler())
	m.session.program = p
	_, err = p.Run()
	if ctx.Err() != nil || errors.Is(err, tea.ErrProgramKilled) {
		return errCancelled
	}
	if err != nil {
		return fmt.Errorf("interactive interface: %w", err)
	}
	if m.err != nil {
		return m.err
	}
	if m.output == "" {
		return errCancelled
	}
	// Nothing reaches stdout until the complete command has succeeded.
	if _, err = fmt.Fprintln(tty, ui.success.Render("✓")+" "+m.confirmation); err != nil {
		return fmt.Errorf("write confirmation: %w", err)
	}
	_, err = fmt.Fprint(os.Stdout, m.output)
	return err
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func export(name, value string) string { return "export " + name + "=" + shellQuote(value) + "\n" }

const unsetProfiles = "unset AWS_PROFILE AWS_DEFAULT_PROFILE\n"
const unsetCredentials = "unset AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN\n"

// AWS-provided labels retain their text and capitalization, but cannot inject
// terminal control sequences into the interface or confirmations.
func terminalText(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value)
}
