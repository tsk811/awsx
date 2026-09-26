package main

import (
	"context"
	"fmt"
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

var accent = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))

// Styles are plain values; Bubble Tea and lipgloss.Fprintln adapt their colors
// to the terminal they write to.
var ui = struct {
	title, accent, selected, muted, warning, failure, success lipgloss.Style
}{
	title:    accent.Bold(true),
	accent:   accent,
	selected: accent.Bold(true).Background(lipgloss.Color("6")).Foreground(lipgloss.Color("0")),
	muted:    lipgloss.NewStyle().Foreground(lipgloss.Color("245")),
	warning:  lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
	failure:  lipgloss.NewStyle().Foreground(lipgloss.Color("1")),
	success:  lipgloss.NewStyle().Foreground(lipgloss.Color("2")),
}

func keyHint(key, action string) string {
	return ui.accent.Bold(true).Render(key) + ui.muted.Render(" "+action)
}

func keyHints(hints ...string) string {
	return strings.Join(hints, ui.muted.Render(" · "))
}

type row struct{ value, name, detail string }

type picker struct {
	input  textinput.Model
	rows   []row
	cursor int
}

func newInput(placeholder string) textinput.Model {
	i := textinput.New()
	i.Prompt = "> "
	i.Placeholder = placeholder
	styles := i.Styles()
	styles.Focused.Prompt = ui.accent
	styles.Focused.Placeholder = ui.muted
	styles.Cursor.Color = lipgloss.Color("6")
	i.SetStyles(styles)
	i.Focus()
	return i
}

func newPicker(rows []row) picker { return picker{input: newInput("Search…"), rows: rows} }

func (p picker) matches() []row {
	var matches []row
	query := strings.ToLower(p.input.Value())
	for _, r := range p.rows {
		if strings.Contains(strings.ToLower(r.name+" "+r.detail), query) {
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
	b.WriteString(p.input.View() + "\n\n")
	matches := p.matches()
	if len(matches) == 0 {
		return b.String() + ui.warning.Render("No matches.") + "\n"
	}
	count := max(1, min(10, height-9))
	start := max(0, min(p.cursor-count/2, len(matches)-count))
	end := min(len(matches), start+count)
	for i := start; i < end; i++ {
		text := terminalText(matches[i].name)
		if i == p.cursor {
			if matches[i].detail != "" {
				text += "  |  " + terminalText(matches[i].detail)
			}
			text = ui.selected.Render("> " + text)
		} else {
			if matches[i].detail != "" {
				text += ui.muted.Render("  |  " + terminalText(matches[i].detail))
			}
			text = "  " + text
		}
		b.WriteString(text + "\n")
	}
	b.WriteString("\n" + ui.accent.Bold(true).Render(fmt.Sprint(p.cursor+1)) + ui.muted.Render(" / ") + ui.accent.Render(fmt.Sprint(len(matches))))
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
		accounts: newPicker(nil), roles: newPicker(nil), spin: spinner.New(spinner.WithStyle(ui.accent)), height: 24,
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
		return tea.Batch(m.spin.Tick, func() tea.Msg {
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
	return textinput.Blink
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
	case authNotice:
		m.status = string(msg)
		return m, nil
	case failureMsg:
		m.err, m.screen = msg.err, "error"
		return m, tea.Quit
	case accountsMsg:
		for _, a := range msg {
			m.accounts.rows = append(m.accounts.rows, row{value: aws.ToString(a.AccountId), name: aws.ToString(a.AccountName), detail: aws.ToString(a.AccountId) + "  |  " + aws.ToString(a.EmailAddress)})
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

func (m *model) View() tea.View {
	var content string
	switch m.screen {
	case "url":
		content = ui.title.Render("AWSX · Initialize") + "\n\nIAM Identity Center start URL\n" + m.url.View() + "\n" + ui.failure.Render(m.validation) + "\n\n" + keyHints(keyHint("Enter", "continue"), keyHint("Esc / Ctrl+C", "cancel")) + "\n"
	case "region", "accounts", "roles":
		heading := "AWS service region"
		context := ""
		if m.command == "init" {
			heading = "IAM Identity Center region"
		}
		if m.screen == "accounts" {
			heading = "Select account"
		}
		if m.screen == "roles" {
			heading = "Select role"
			context = terminalText(m.account.name) + " (" + terminalText(m.account.value) + ")\n"
		}
		content = ui.title.Render("AWSX · "+heading) + "\n" + ui.muted.Render(context) + "\n" + m.activePicker().view(m.height) + "\n\n" + keyHints(keyHint("Type", "to search"), keyHint("↑/↓", "move"), keyHint("Enter", "select"), keyHint("Esc", "clear/back/cancel"), keyHint("Ctrl+C", "cancel")) + "\n"
	case "loading":
		content = ui.title.Render("AWSX") + "\n\n" + m.spin.View() + " " + m.status + "\n\n" + keyHint("Esc / Ctrl+C", "cancel") + "\n"
	case "error":
		content = ui.failure.Bold(true).Render("AWSX · Error") + "\n"
	}
	v := tea.NewView(content)
	// The inline renderer leaves stale rows behind when frames shrink; the
	// alt screen is restored intact on exit, so no UI remnants reach the shell.
	// The error frame is drawn inline so it stays above the error message.
	v.AltScreen = m.screen != "error"
	return v
}
