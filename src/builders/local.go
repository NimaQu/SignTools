package builders

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// LocalData configures signing inside the SignTools service itself (no external builder).
type LocalData struct {
	Enable bool `yaml:"enable"`
	// the Linux signing script, see signer/sign.py
	SignScript string `yaml:"sign_script"`
	// where jobs are extracted and re-packed, needs room for ~2x the largest app; default <save_dir>/work
	WorkDir string `yaml:"work_dir"`
	// interpreter for the script
	Python string `yaml:"python"`
}

// LocalServerUrl is how the signing script reaches this service; set by main once the port is known.
var LocalServerUrl = "http://127.0.0.1:8080"

const localLogsKept = 30

type LocalJobLog struct {
	Started  time.Time
	Finished time.Time
	Running  bool
	Failed   bool
	Output   *bytes.Buffer
}

type Local struct {
	*LocalData
	queue   chan struct{}
	mu      sync.Mutex
	secrets map[string]string
	logs    []*LocalJobLog
	// TimeoutMins of one job, set from sign_timeout_mins
	TimeoutMins uint64
}

func MakeLocal(data *LocalData) *Local {
	l := &Local{LocalData: data, queue: make(chan struct{}, 1000), secrets: map[string]string{}}
	go l.worker()
	return l
}

func (l *Local) Trigger() error {
	select {
	case l.queue <- struct{}{}:
		return nil
	default:
		return fmt.Errorf("local signing queue full")
	}
}

func (l *Local) SetSecrets(secrets map[string]string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.secrets = secrets
	return nil
}

func (l *Local) GetStatusUrl() (string, error) {
	return "/local-builder", nil
}

// Logs returns the most recent jobs first.
func (l *Local) Logs() []LocalJobLog {
	l.mu.Lock()
	defer l.mu.Unlock()
	result := make([]LocalJobLog, 0, len(l.logs))
	for i := len(l.logs) - 1; i >= 0; i-- {
		entry := *l.logs[i]
		entry.Output = bytes.NewBuffer(append([]byte(nil), l.logs[i].Output.Bytes()...))
		result = append(result, entry)
	}
	return result
}

// lockedWriter appends process output to a job log under the builder's lock.
type lockedWriter struct {
	l   *Local
	buf *bytes.Buffer
}

func (w lockedWriter) Write(p []byte) (int, error) {
	w.l.mu.Lock()
	defer w.l.mu.Unlock()
	return w.buf.Write(p)
}

func (l *Local) worker() {
	for range l.queue {
		entry := &LocalJobLog{Started: time.Now(), Running: true, Output: &bytes.Buffer{}}
		l.mu.Lock()
		l.logs = append(l.logs, entry)
		if len(l.logs) > localLogsKept {
			l.logs = l.logs[len(l.logs)-localLogsKept:]
		}
		secrets := l.secrets
		l.mu.Unlock()

		err := l.runJob(secrets, lockedWriter{l, entry.Output})
		l.mu.Lock()
		entry.Running = false
		entry.Finished = time.Now()
		if err != nil {
			entry.Failed = true
			fmt.Fprintf(entry.Output, "\n%v\n", err)
		}
		l.mu.Unlock()
		if err != nil {
			log.Err(err).Msg("local sign job")
		}
	}
}

func (l *Local) runJob(secrets map[string]string, out lockedWriter) error {
	if err := os.MkdirAll(l.WorkDir, 0700); err != nil {
		return err
	}
	jobDir, err := os.MkdirTemp(l.WorkDir, "job-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(jobDir)

	timeout := time.Duration(l.TimeoutMins) * time.Minute
	if timeout <= 0 {
		timeout = 60 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	script, err := filepath.Abs(l.SignScript)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, l.Python, script)
	cmd.Dir = jobDir
	cmd.Env = append(os.Environ(), "PYTHONUNBUFFERED=1")
	for k, v := range secrets {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	// talk to this service over loopback, whatever its public server_url is
	cmd.Env = append(cmd.Env, "SECRET_URL="+LocalServerUrl, "SIGNTOOLS_INTERNAL_URL="+LocalServerUrl)
	cmd.Stdout = out
	cmd.Stderr = out
	return cmd.Run()
}
