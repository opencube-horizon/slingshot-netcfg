// Package sdnotify sends service state notifications to systemd (sd_notify(3),
// spec §6.10). Without $NOTIFY_SOCKET, as on Talos, every notification is a
// no-op.
package sdnotify

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"time"
)

// Notification states.
const (
	Ready    = "READY=1"
	Stopping = "STOPPING=1"
	Watchdog = "WATCHDOG=1"
)

// Status returns the STATUS= notification for a free-form status line.
func Status(s string) string { return "STATUS=" + s }

// Notifier is connected to systemd's notification socket.
type Notifier struct {
	conn     *net.UnixConn
	watchdog time.Duration
}

// FromEnv connects to $NOTIFY_SOCKET and reads the watchdog interval from
// $WATCHDOG_USEC (if $WATCHDOG_PID, when set, is this process). It removes
// these variables from the environment, so that child processes (lldpcli)
// don't inherit them. Without $NOTIFY_SOCKET it returns a Notifier that
// does nothing.
func FromEnv() (*Notifier, error) {
	path := os.Getenv("NOTIFY_SOCKET")
	usec := os.Getenv("WATCHDOG_USEC")
	pid := os.Getenv("WATCHDOG_PID")
	for _, k := range []string{"NOTIFY_SOCKET", "WATCHDOG_USEC", "WATCHDOG_PID"} {
		if err := os.Unsetenv(k); err != nil {
			return nil, fmt.Errorf("unset %s: %w", k, err)
		}
	}
	n := &Notifier{}
	if path == "" {
		return n, nil
	}
	// A leading "@" names an abstract socket; net handles that prefix.
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		return nil, fmt.Errorf("connect to NOTIFY_SOCKET %s: %w", path, err)
	}
	n.conn = conn
	if usec != "" && (pid == "" || pid == strconv.Itoa(os.Getpid())) {
		v, err := strconv.ParseUint(usec, 10, 63)
		if err != nil || v == 0 {
			return nil, fmt.Errorf("invalid WATCHDOG_USEC %q", usec)
		}
		n.watchdog = time.Duration(v) * time.Microsecond //nolint:gosec // G115: ParseUint bit size 63 fits int64.
	}
	return n, nil
}

// Notify sends one state notification.
func (n *Notifier) Notify(state string) error {
	if n.conn == nil {
		return nil
	}
	if _, err := n.conn.Write([]byte(state)); err != nil {
		return fmt.Errorf("sd_notify %s: %w", state, err)
	}
	return nil
}

// WatchdogInterval is the systemd watchdog timeout, or 0 if the watchdog is off.
func (n *Notifier) WatchdogInterval() time.Duration { return n.watchdog }

// Close closes the connection to systemd.
func (n *Notifier) Close() error {
	if n.conn == nil {
		return nil
	}
	return n.conn.Close()
}
