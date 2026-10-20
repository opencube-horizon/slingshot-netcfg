package sdnotify

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// listen creates a socket standing in for systemd's and points $NOTIFY_SOCKET at it.
func listen(t *testing.T) *net.UnixConn {
	t.Helper()
	path := filepath.Join(t.TempDir(), "notify")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	t.Setenv("NOTIFY_SOCKET", path)
	return conn
}

func TestNotify_sendsStateToNotifySocket(t *testing.T) {
	sock := listen(t)
	n, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Close() })

	if err := n.Notify(Ready); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 64)
	if err := sock.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	k, err := sock.Read(buf)
	if err != nil || string(buf[:k]) != Ready {
		t.Errorf("received %q, %v; want %q", buf[:k], err, Ready)
	}
}

func TestFromEnv_removesVariablesFromEnvironment(t *testing.T) {
	listen(t)
	t.Setenv("WATCHDOG_USEC", "30000000")
	t.Setenv("WATCHDOG_PID", strconv.Itoa(os.Getpid()))

	n, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Close() })

	for _, k := range []string{"NOTIFY_SOCKET", "WATCHDOG_USEC", "WATCHDOG_PID"} {
		if v, ok := os.LookupEnv(k); ok {
			t.Errorf("%s=%q still set", k, v)
		}
	}
}

func TestFromEnv_watchdogInterval(t *testing.T) {
	for _, tc := range []struct {
		name, pid string
		want      time.Duration
	}{
		{"for this process", strconv.Itoa(os.Getpid()), 30 * time.Second},
		{"PID unset", "", 30 * time.Second},
		{"for another process", "1", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listen(t)
			t.Setenv("WATCHDOG_USEC", "30000000")
			t.Setenv("WATCHDOG_PID", tc.pid)

			n, err := FromEnv()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = n.Close() })

			if got := n.WatchdogInterval(); got != tc.want {
				t.Errorf("WatchdogInterval() = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestNotify_isNoOp_withoutNotifySocket(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "")

	n, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}

	if err := n.Notify(Ready); err != nil || n.WatchdogInterval() != 0 {
		t.Errorf("Notify = %v, WatchdogInterval = %s; want no-op", err, n.WatchdogInterval())
	}
}
