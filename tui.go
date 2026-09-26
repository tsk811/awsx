package main

import (
	"context"
	"fmt"
	"image/color"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sso"
	ssotypes "github.com/aws/aws-sdk-go-v2/service/sso/types"
	"github.com/aws/aws-sdk-go-v2/service/ssooidc"
)

// Styles are plain values; Bubble Tea and lipgloss.Fprintln adapt their colors
// to the terminal they write to. ui starts with the dark-background palette
// and is rebuilt once the terminal reports its background color.
var ui = newTheme(true)

type theme struct {
	accent, text, muted, faint, failure, success, badge, key lipgloss.Style
}

// newTheme builds the amber theme for a dark or light terminal background.
func newTheme(dark bool) theme {
	c := func(onLight, onDark string) color.Color {
		return lipgloss.LightDark(dark)(lipgloss.Color(onLight), lipgloss.Color(onDark))
	}
	return theme{
		accent:  lipgloss.NewStyle().Foreground(c("#C45500", "#FF9900")),
		text:    lipgloss.NewStyle().Foreground(c("#16191F", "#E6E6E6")),
		muted:   lipgloss.NewStyle().Foreground(c("#5F6B7A", "#8D99A8")),
		faint:   lipgloss.NewStyle().Foreground(c("#9BA7B6", "#545B64")),
		failure: lipgloss.NewStyle().Foreground(c("#D91515", "#FF5D64")),
		success: lipgloss.NewStyle().Foreground(c("#037F0C", "#29AD32")),
		badge:   lipgloss.NewStyle().Bold(true).Background(lipgloss.Color("#FF9900")).Foreground(lipgloss.Color("#232F3E")).Padding(0, 1),
		key:     lipgloss.NewStyle().Background(c("#E9EBED", "#2A2E33")).Foreground(c("#16191F", "#E6E6E6")).Padding(0, 1),
	}
}

func keyHint(key, action string) string { return ui.key.Render(key) + " " + ui.muted.Render(action) }

func keyHints(hints ...string) string { return strings.Join(hints, "  ") }

func pad(s string, width int) string { return s + strings.Repeat(" ", max(0, width-lipgloss.Width(s))) }

// detail and extra are secondary columns: an account's ID and email, or a
// region's name.
type row struct{ value, name, detail, extra string }

type picker struct {
	input  textinput.Model
	rows   []row
	cursor int
}

func styleInput(i *textinput.Model) {
	styles := i.Styles()
	styles.Focused.Prompt = ui.accent.Bold(true)
	styles.Focused.Placeholder = ui.faint
	styles.Focused.Text = ui.text
	styles.Cursor.Color = ui.accent.GetForeground()
	i.SetStyles(styles)
}

func newInput(placeholder string) textinput.Model {
	i := textinput.New()
	i.Prompt = "❯ "
	i.Placeholder = placeholder
	styleInput(&i)
	i.Focus()
	return i
}

func newPicker(rows []row) picker { return picker{input: newInput("Search…"), rows: rows} }

func (p picker) matches() []row {
	var matches []row
	query := strings.ToLower(p.input.Value())
	for _, r := range p.rows {
		if strings.Contains(strings.ToLower(r.name+" "+r.detail+" "+r.extra), query) {
			matches = append(matches, r)
		}
	}
	return matches
}

func (p *picker) update(msg tea.Msg) tea.Cmd {
	before := p.input.Value()
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	if before != p.input.Value() {
		p.cursor = 0
	}
	return cmd
}

func (p *picker) move(delta int) { p.cursor = max(0, min(p.cursor+delta, len(p.matches())-1)) }

func (p picker) view(height int) string {
	var b strings.Builder
	b.WriteString(" " + p.input.View() + "\n\n")
	matches := p.matches()
	if len(matches) == 0 {
		return b.String() + "   " + ui.muted.Render("No matches for “"+terminalText(p.input.Value())+"”")
	}
	nameWidth := 0
	for _, r := range matches {
		nameWidth = max(nameWidth, lipgloss.Width(terminalText(r.name)))
	}
	count := max(1, min(10, height-12))
	start := max(0, min(p.cursor-count/2, len(matches)-count))
	end := min(len(matches), start+count)
	for i := start; i < end; i++ {
		name := pad(terminalText(matches[i].name), nameWidth+3)
		detail, extra := terminalText(matches[i].detail), terminalText(matches[i].extra)
		var line string
		if i == p.cursor {
			line = ui.accent.Render("▌ ") + ui.accent.Bold(true).Render(name) + ui.text.Render(detail)
			if extra != "" {
				line += "   " + ui.muted.Render(extra)
			}
		} else {
			line = "  " + ui.text.Render(name) + ui.muted.Render(detail)
			if extra != "" {
				line += "   " + ui.faint.Render(extra)
			}
		}
		b.WriteString(" " + line + "\n")
	}
	b.WriteString("\n " + ui.faint.Render(fmt.Sprintf("%d of %d", p.cursor+1, len(matches))))
	return b.String()
}

type accountsMsg []ssotypes.AccountInfo
type rolesMsg []ssotypes.RoleInfo
type credentialsMsg struct{ credentials *ssotypes.RoleCredentials }
type initializedMsg struct{}
type failureMsg struct{ err error }

type model struct {
	ctx                      context.Context
	cancel                   context.CancelFunc
	command, screen, status  string
	path, startURL           string
	url                      textinput.Model
	regions, accounts, roles picker
	account                  row
	role                     string
	roleAccount              string
	spin                     spinner.Model
	height                   int
	session                  *session
	err                      error
	validation               string
	output, confirmation     string
}

func newModel(ctx context.Context, cancel context.CancelFunc, command, path string, c config) *model {
	var regionRows []row
	for _, r := range regions {
		regionRows = append(regionRows, row{value: r.code, name: r.code, detail: r.name})
	}
	m := &model{
		ctx: ctx, cancel: cancel, command: command, path: path,
		url: newInput("https://company.awsapps.com/start"), regions: newPicker(regionRows),
		accounts: newPicker(nil), roles: newPicker(nil), spin: spinner.New(spinner.WithSpinner(spinner.Points), spinner.WithStyle(ui.accent)), height: 24,
		session: &session{config: c, path: path},
	}
	switch command {
	case "init":
		m.screen = "url"
	case "region":
		m.screen = "region"
	case "login":
		m.screen, m.status = "loading", "Authenticating with IAM Identity Center…"
		// These public/bearer APIs need no shared AWS config, profile, or IAM credentials.
		m.session.oidc = ssooidc.New(ssooidc.Options{Region: c.Region})
		m.session.sso = sso.New(sso.Options{Region: c.Region})
	}
	return m
}

func (m *model) Init() tea.Cmd {
	if m.command == "login" {
		return tea.Batch(tea.RequestBackgroundColor, m.spin.Tick, func() tea.Msg {
			if err := m.session.authenticate(m.ctx, false); err != nil {
				return failureMsg{err}
			}
			m.session.notice("Loading all accounts…")
			accounts, err := m.session.accounts(m.ctx)
			if err != nil {
				return failureMsg{err}
			}
			return accountsMsg(accounts)
		})
	}
	return tea.Batch(tea.RequestBackgroundColor, textinput.Blink)
}

// restyle reapplies ui to the components that copied its styles.
func (m *model) restyle() {
	for _, i := range []*textinput.Model{&m.url, &m.regions.input, &m.accounts.input, &m.roles.input} {
		styleInput(i)
	}
	m.spin.Style = ui.accent
}

func (m *model) activePicker() *picker {
	switch m.screen {
	case "region":
		return &m.regions
	case "accounts":
		return &m.accounts
	case "roles":
		return &m.roles
	}
	return nil
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// A cancelled request can still deliver its final message while Quit is
	// queued. Once cancelled or failed, the final frame and m.err stay as set.
	if m.err != nil {
		return m, nil
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.height = msg.Height
		width := max(1, msg.Width-4)
		m.url.SetWidth(width)
		m.regions.input.SetWidth(width)
		m.accounts.input.SetWidth(width)
		m.roles.input.SetWidth(width)
		return m, nil
	case tea.BackgroundColorMsg:
		ui = newTheme(msg.IsDark())
		m.restyle()
		return m, nil
	case authNotice:
		m.status = string(msg)
		return m, nil
	case failureMsg:
		m.err, m.screen = msg.err, "error"
		return m, tea.Quit
	case accountsMsg:
		for _, a := range msg {
			m.accounts.rows = append(m.accounts.rows, row{value: aws.ToString(a.AccountId), name: aws.ToString(a.AccountName), detail: aws.ToString(a.AccountId), extra: aws.ToString(a.EmailAddress)})
		}
		m.screen = "accounts"
		return m, textinput.Blink
	case rolesMsg:
		m.roles.rows = nil
		for _, r := range msg {
			m.roles.rows = append(m.roles.rows, row{value: aws.ToString(r.RoleName), name: aws.ToString(r.RoleName)})
		}
		m.roles.move(0)
		m.screen = "roles"
		return m, textinput.Blink
	case credentialsMsg:
		c := msg.credentials
		m.output = export("AWS_ACCESS_KEY_ID", aws.ToString(c.AccessKeyId)) + export("AWS_SECRET_ACCESS_KEY", aws.ToString(c.SecretAccessKey)) + export("AWS_SESSION_TOKEN", aws.ToString(c.SessionToken)) + unsetProfiles
		m.confirmation = fmt.Sprintf("Logged in to %s (%s), role %s. Credentials expire %s.", terminalText(m.account.name), terminalText(m.account.value), terminalText(m.role), time.UnixMilli(c.Expiration).UTC().Format(time.RFC3339))
		m.screen = "done"
		return m, tea.Quit
	case initializedMsg:
		m.output, m.confirmation, m.screen = unsetCredentials+unsetProfiles, "Configuration saved. Run awsx login to authenticate.", "done"
		return m, tea.Quit
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		if m.screen != "loading" {
			return m, nil
		}
		return m, cmd
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			return m.quit()
		}
		if msg.String() == "esc" {
			if p := m.activePicker(); p != nil && p.input.Value() != "" {
				p.input.SetValue("")
				p.cursor = 0
				return m, nil
			}
			switch m.screen {
			case "roles":
				m.screen = "accounts"
				return m, textinput.Blink
			case "region":
				if m.command == "init" {
					m.screen = "url"
					return m, textinput.Blink
				}
			}
			return m.quit()
		}
		if p := m.activePicker(); p != nil {
			switch msg.String() {
			case "up":
				p.move(-1)
				return m, nil
			case "down":
				p.move(1)
				return m, nil
			case "enter":
				matches := p.matches()
				if len(matches) == 0 {
					return m, nil
				}
				return m.choose(matches[p.cursor])
			}
		}
		if m.screen == "url" && msg.String() == "enter" {
			value := strings.TrimSpace(m.url.Value())
			if !validStartURL(value) {
				m.validation = "Enter a valid HTTPS start URL."
				return m, nil
			}
			m.startURL, m.screen, m.validation = value, "region", ""
			return m, textinput.Blink
		}
	}
	if p := m.activePicker(); p != nil {
		return m, p.update(msg)
	}
	if m.screen == "url" {
		var cmd tea.Cmd
		m.url, cmd = m.url.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *model) quit() (tea.Model, tea.Cmd) {
	m.err = errCancelled
	m.screen = "done"
	m.cancel()
	return m, tea.Quit
}

func (m *model) choose(r row) (tea.Model, tea.Cmd) {
	switch m.screen {
	case "region":
		if m.command == "region" {
			m.output = export("AWS_REGION", r.value) + export("AWS_DEFAULT_REGION", r.value)
			m.confirmation, m.screen = "AWS region set to "+r.value+".", "done"
			return m, tea.Quit
		}
		m.screen, m.status = "loading", "Saving configuration…"
		return m, tea.Batch(m.spin.Tick, func() tea.Msg {
			if m.ctx.Err() != nil {
				return failureMsg{errCancelled}
			}
			if err := saveConfig(m.path, config{StartURL: m.startURL, Region: r.value}); err != nil {
				return failureMsg{err}
			}
			return initializedMsg{}
		})
	case "accounts":
		if m.roleAccount != r.value {
			m.roles.input.SetValue("")
			m.roles.cursor = 0
			m.roleAccount = r.value
		}
		m.account = r
		m.screen, m.status = "loading", "Loading all roles for "+terminalText(r.name)+" ("+terminalText(r.value)+")…"
		return m, tea.Batch(m.spin.Tick, func() tea.Msg {
			roles, err := m.session.roles(m.ctx, r.value)
			if err != nil {
				return failureMsg{err}
			}
			return rolesMsg(roles)
		})
	case "roles":
		m.role = r.value
		m.screen, m.status = "loading", "Retrieving temporary role credentials…"
		accountID := m.account.value
		return m, tea.Batch(m.spin.Tick, func() tea.Msg {
			credentials, err := m.session.credentials(m.ctx, accountID, r.value)
			if err != nil {
				return failureMsg{err}
			}
			return credentialsMsg{credentials}
		})
	}
	return m, nil
}

// header is the awsx badge followed by the command's steps, with the current
// step highlighted.
func (m *model) header() string {
	steps, current := []string{"region"}, 0
	switch m.command {
	case "init":
		steps, current = []string{"init", "start url", "region"}, 1
		if m.screen != "url" {
			current = 2
		}
	case "login":
		steps = []string{"login", "account", "role"}
		switch {
		case m.screen == "roles" || m.role != "":
			current = 2
		case len(m.accounts.rows) > 0:
			current = 1
		}
	}
	for i, step := range steps {
		switch {
		case i == current:
			steps[i] = ui.accent.Bold(true).Render(step)
		case i < current:
			steps[i] = ui.muted.Render(step)
		default:
			steps[i] = ui.faint.Render(step)
		}
	}
	return ui.badge.Render("awsx") + "  " + strings.Join(steps, ui.faint.Render(" › "))
}

func (m *model) View() tea.View {
	content := "\n " + m.header() + "\n\n"
	switch m.screen {
	case "url":
		content += " " + ui.muted.Render("IAM Identity Center start URL") + "\n " + m.url.View() + "\n " + ui.failure.Render(m.validation) + "\n\n " + keyHints(keyHint("enter", "continue"), keyHint("esc", "cancel")) + "\n"
	case "region", "accounts", "roles":
		context := ""
		switch {
		case m.screen == "roles":
			context = ui.muted.Render("account ") + ui.text.Bold(true).Render(terminalText(m.account.name)) + ui.faint.Render("  "+terminalText(m.account.value))
		case m.screen == "region" && m.command == "init":
			context = ui.muted.Render("IAM Identity Center region")
		case m.screen == "region":
			context = ui.muted.Render("AWS service region")
		}
		if context != "" {
			content += " " + context + "\n\n"
		}
		content += m.activePicker().view(m.height) + "\n\n " + keyHints(keyHint("↑↓", "move"), keyHint("enter", "select"), keyHint("esc", "clear/back"), keyHint("ctrl+c", "cancel")) + "\n"
	case "loading":
		content += " " + lipgloss.JoinHorizontal(lipgloss.Top, m.spin.View()+" ", ui.text.Render(m.status)) + "\n\n " + keyHint("esc", "cancel") + "\n"
	case "error":
		content = ui.badge.Render("awsx") + " " + ui.failure.Bold(true).Render("error") + "\n"
	}
	v := tea.NewView(content)
	// The inline renderer leaves stale rows behind when frames shrink; the
	// alt screen is restored intact on exit, so no UI remnants reach the shell.
	// The error frame is drawn inline so it stays above the error message.
	v.AltScreen = m.screen != "error"
	return v
}
