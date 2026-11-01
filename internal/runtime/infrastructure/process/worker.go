package process

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"

	"organon/internal/runtime/application"
	"organon/platform/egress"
)

// maxFrame bounds a frame from a worker.
const maxFrame = 16 << 20

// bootstrap puts organon on sys.path under -I (which ignores PYTHONPATH and
// the working directory) and runs the worker loop.
const bootstrap = "import sys; sys.path.insert(0, sys.argv[1]); from organon.worker import main; sys.exit(main(sys.argv[2], sys.argv[3]))"

// Starter starts workers as OS processes: own process group, empty
// environment (no credentials or app secrets), private temporary
// directory, address-space and open-file limits, and outbound HTTP only
// through an allowlisting egress proxy (spec 11.3). Code that ignores the
// proxy variables is not contained at this level; use container isolation
// for untrusted code.
type Starter struct {
	Envs *Envs
	Log  *slog.Logger
	// AllowPrivate lets the proxy reach private addresses (tests only).
	AllowPrivate bool
}

var _ application.WorkerStarter = (*Starter)(nil)

// Start implements application.WorkerStarter.
func (s *Starter) Start(_ context.Context, spec application.WorkerSpec) (application.Worker, error) {
	organon, err := s.Envs.FwnodeDir()
	if err != nil {
		return nil, err
	}
	tmpRoot := filepath.Join(s.Envs.Root, "tmp")
	if err := os.MkdirAll(tmpRoot, 0o700); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp(tmpRoot, "w-")
	if err != nil {
		return nil, err
	}
	proxy, err := egress.NewProxy(spec.Limits.Network, s.AllowPrivate)
	if err != nil {
		_ = os.RemoveAll(tmp)
		return nil, err
	}
	memMb := spec.Limits.MemoryMb
	if memMb <= 0 {
		memMb = 512
	}
	// The worker starts from a context independent of the request: it
	// outlives the execution that started it.
	cmd := exec.Command("/bin/sh", "-c", `ulimit -v "$1" 2>/dev/null; ulimit -n 256 2>/dev/null; shift; exec "$@"`,
		"sh", strconv.FormatInt(memMb*1024, 10), spec.Env.Python, "-I", "-c", bootstrap, organon, spec.Env.Dir, spec.Entry)
	cmd.Dir = tmp
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + tmp, "TMPDIR=" + tmp, "LANG=C.UTF-8",
		"PYTHONDONTWRITEBYTECODE=1", "PYTHONUNBUFFERED=1",
		"HTTP_PROXY=" + proxy.URL(), "HTTPS_PROXY=" + proxy.URL(), "http_proxy=" + proxy.URL(), "https_proxy=" + proxy.URL()}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = proxy.Close()
		_ = os.RemoveAll(tmp)
		return nil, err
	}
	w := &worker{cmd: cmd, in: stdin, out: bufio.NewReader(stdout), proxy: proxy, tmp: tmp, done: make(chan struct{})}
	log := s.Log
	if log == nil {
		log = slog.Default()
	}
	go func() { // user output goes to the log
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			log.Info("code worker output", "entry", spec.Entry, "pid", cmd.Process.Pid, "line", sc.Text())
		}
	}()
	go func() {
		_ = cmd.Wait()
		close(w.done)
		_ = proxy.Close()
		_ = os.RemoveAll(tmp)
	}()
	return w, nil
}

type worker struct {
	cmd   *exec.Cmd
	in    io.WriteCloser
	out   *bufio.Reader
	proxy *egress.Proxy
	tmp   string
	once  sync.Once
	done  chan struct{}
	wmu   sync.Mutex
}

func (w *worker) Send(f application.Frame) error {
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	w.wmu.Lock()
	defer w.wmu.Unlock()
	var head [4]byte
	binary.BigEndian.PutUint32(head[:], uint32(len(data)))
	if _, err := w.in.Write(append(head[:], data...)); err != nil {
		return fmt.Errorf("worker exited: %w", err)
	}
	return nil
}

func (w *worker) Recv() (application.Frame, error) {
	var head [4]byte
	if _, err := io.ReadFull(w.out, head[:]); err != nil {
		return application.Frame{}, fmt.Errorf("worker exited: %w", err)
	}
	n := binary.BigEndian.Uint32(head[:])
	if n > maxFrame {
		w.Kill()
		return application.Frame{}, fmt.Errorf("worker frame of %d bytes exceeds %d", n, maxFrame)
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(w.out, data); err != nil {
		return application.Frame{}, fmt.Errorf("worker exited: %w", err)
	}
	var f application.Frame
	if err := json.Unmarshal(data, &f); err != nil {
		return application.Frame{}, fmt.Errorf("invalid worker frame: %w", err)
	}
	return f, nil
}

// Kill stops the whole process group; pending Recv calls return.
func (w *worker) Kill() {
	w.once.Do(func() {
		if w.cmd.Process != nil {
			_ = syscall.Kill(-w.cmd.Process.Pid, syscall.SIGKILL)
		}
		_ = w.in.Close()
	})
}
