// Package portal drives signtools-portal (Apple ID login, teams) for the web UI.
package portal

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/pkg/errors"
)

type Team struct {
	Id     string `json:"id"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Status string `json:"status"`
}

type Phone struct {
	Id            int    `json:"id"`
	LastTwoDigits string `json:"last_two_digits"`
}

type Account struct {
	Email     string `json:"email"`
	FirstName string `json:"first_name"`
}

// Event is one JSON line printed by signtools-portal.
type Event struct {
	Ok       bool      `json:"ok"`
	Event    string    `json:"event"`
	Error    string    `json:"error"`
	Code     *int64    `json:"code"`
	Email    string    `json:"email"`
	Method   string    `json:"method"`
	Phones   []Phone   `json:"phones"`
	Teams    []Team    `json:"teams"`
	Accounts []Account `json:"accounts"`
}

type Client struct {
	Bin     string
	DataDir string
	// extra environment, e.g. ANISETTE_URL_V3
	Env []string
	// how long a login waits for Apple (and for the user to type the 2FA code)
	StepTimeout time.Duration

	mu     sync.Mutex
	logins map[string]*Login
}

func (c *Client) env() []string {
	return append(append(os.Environ(), "PORTAL_DATA="+c.DataDir), c.Env...)
}

// Run executes a one-shot command and returns its result.
func (c *Client) Run(args ...string) (*Event, error) {
	cmd := exec.Command(c.Bin, args...)
	cmd.Env = c.env()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	ev, parseErr := lastEvent(stdout.Bytes())
	if parseErr != nil {
		if runErr != nil {
			return nil, errors.Errorf("%s: %v: %s", c.Bin, runErr, bytes.TrimSpace(stderr.Bytes()))
		}
		return nil, parseErr
	}
	if !ev.Ok {
		return ev, errors.New(ev.Error)
	}
	return ev, nil
}

func lastEvent(out []byte) (*Event, error) {
	var last *Event
	for _, line := range bytes.Split(out, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var ev Event
		if err := json.Unmarshal(line, &ev); err == nil {
			last = &ev
		}
	}
	if last == nil {
		return nil, errors.New("no result from signtools-portal")
	}
	return last, nil
}

func (c *Client) Accounts() ([]Account, error) {
	ev, err := c.Run("accounts")
	if err != nil {
		return nil, err
	}
	return ev.Accounts, nil
}

func (c *Client) Teams(email string) ([]Team, error) {
	ev, err := c.Run("teams", "--email", email)
	if err != nil {
		return nil, err
	}
	return ev.Teams, nil
}

func (c *Client) Logout(email string) error {
	_, err := c.Run("logout", "--email", email)
	return err
}

// Login is an interactive login in progress (waiting for a 2FA code).
type Login struct {
	Id     string
	Email  string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	events chan Event
	done   chan struct{}
	stderr bytes.Buffer
}

// StartLogin begins logging in and returns the first event: "2fa", "done" or "error".
func (c *Client) StartLogin(email, password string) (*Login, *Event, error) {
	cmd := exec.Command(c.Bin, "login")
	cmd.Env = c.env()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	l := &Login{Id: uuid.NewString(), Email: email, cmd: cmd, stdin: stdin, events: make(chan Event, 4), done: make(chan struct{})}
	cmd.Stderr = &l.stderr
	if err := cmd.Start(); err != nil {
		return nil, nil, errors.WithMessage(err, "start signtools-portal")
	}
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 1<<20), 1<<20)
		for scanner.Scan() {
			var ev Event
			if json.Unmarshal(scanner.Bytes(), &ev) == nil && ev.Event != "" {
				l.events <- ev
			}
		}
		_ = cmd.Wait()
		close(l.done)
	}()
	creds, _ := json.Marshal(map[string]string{"email": email, "password": password})
	if _, err := fmt.Fprintf(stdin, "%s\n", creds); err != nil {
		l.kill()
		return nil, nil, err
	}
	ev, err := c.next(l)
	if err != nil {
		return nil, nil, err
	}
	if ev.Event == "2fa" {
		c.mu.Lock()
		if c.logins == nil {
			c.logins = map[string]*Login{}
		}
		c.logins[l.Id] = l
		c.mu.Unlock()
		// give up on abandoned logins
		go func() {
			select {
			case <-l.done:
			case <-time.After(15 * time.Minute):
				l.kill()
			}
			c.mu.Lock()
			delete(c.logins, l.Id)
			c.mu.Unlock()
		}()
	}
	return l, ev, nil
}

// GetLogin returns a login waiting for its 2FA code.
func (c *Client) GetLogin(id string) (*Login, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	l, ok := c.logins[id]
	return l, ok
}

// SubmitCode sends the 2FA code; RequestSms asks Apple to send one to a trusted phone number instead.
func (c *Client) SubmitCode(l *Login, code string) (*Event, error) {
	return c.reply(l, map[string]any{"code": code})
}

func (c *Client) RequestSms(l *Login, phoneId int) (*Event, error) {
	return c.reply(l, map[string]any{"sms": phoneId})
}

func (c *Client) reply(l *Login, msg map[string]any) (*Event, error) {
	data, _ := json.Marshal(msg)
	if _, err := fmt.Fprintf(l.stdin, "%s\n", data); err != nil {
		return nil, errors.WithMessage(err, "login process is gone, start again")
	}
	return c.next(l)
}

func (c *Client) next(l *Login) (*Event, error) {
	timeout := c.StepTimeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	select {
	case ev := <-l.events:
		if ev.Event != "2fa" {
			c.mu.Lock()
			delete(c.logins, l.Id)
			c.mu.Unlock()
		}
		return &ev, nil
	case <-l.done:
		select {
		case ev := <-l.events:
			return &ev, nil
		default:
		}
		return nil, errors.Errorf("signtools-portal exited: %s", bytes.TrimSpace(l.stderr.Bytes()))
	case <-time.After(timeout):
		l.kill()
		return nil, errors.New("timed out waiting for Apple")
	}
}

func (l *Login) kill() {
	if l.cmd.Process != nil {
		_ = l.cmd.Process.Kill()
	}
}
