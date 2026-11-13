package testdb

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

const (
	postgresManagedLabel      = "io.ovumcy.test.postgres"
	postgresOwnerPIDLabel     = "io.ovumcy.test.owner-pid"
	postgresOwnerStartLabel   = "io.ovumcy.test.owner-start"
	postgresDatabaseNameLabel = "io.ovumcy.test.database"
)

// postgresContainerLabels identify the test process that owns a Postgres
// container. Docker's --rm only removes a container when its client exits
// normally; if the test process is killed, that cleanup never runs. The owner
// identity lets a later test run safely distinguish such an orphan from a
// container belonging to another test process that is still running.
func postgresContainerLabels(databaseName string) []string {
	pid, startID := postgresOwnerIdentity()
	return []string{
		"--label", postgresManagedLabel + "=true",
		"--label", postgresOwnerPIDLabel + "=" + pid,
		"--label", postgresOwnerStartLabel + "=" + startID,
		"--label", postgresDatabaseNameLabel + "=" + databaseName,
	}
}

// cleanupStalePostgresContainers removes Postgres test containers whose owner
// process no longer exists. A live owner is always preserved, including when
// multiple test binaries run concurrently. Containers without a valid owner
// identity are left alone: an unknown owner must never be treated as proof
// that another test is not using the container.
func cleanupStalePostgresContainers(t testingT) {
	t.Helper()

	output, err := runDockerCommandWithError(
		"ps", "-aq", "--filter", "label="+postgresManagedLabel+"=true",
	)
	if err != nil {
		t.Fatalf("list managed postgres test containers: %v", err)
		return
	}

	for _, containerID := range strings.Fields(output) {
		labels, err := inspectPostgresContainerLabels(containerID)
		if err != nil {
			// The container may have exited and disappeared between the list
			// and inspect calls. Any other inspection failure is non-fatal for
			// cleanup; without trustworthy labels, preserving the container is
			// the safe choice.
			continue
		}
		alive, known := postgresOwnerState(labels)
		if !known || alive {
			continue
		}
		_ = runDockerCommandAllowFailure("rm", "-f", "-v", containerID)
	}
}

func inspectPostgresContainerLabels(containerID string) (map[string]string, error) {
	output, err := runDockerCommandWithError(
		"inspect", "--format={{json .Config.Labels}}", containerID,
	)
	if err != nil {
		return nil, err
	}

	labels := make(map[string]string)
	if err := json.Unmarshal([]byte(output), &labels); err != nil {
		return nil, err
	}
	return labels, nil
}

func postgresOwnerIdentity() (pid string, startID string) {
	pid = strconv.Itoa(os.Getpid())
	startID = postgresProcessStartID(os.Getpid())
	return pid, startID
}

// postgresOwnerState reports whether a container's owner is alive. The second
// result is false when ownership cannot be established. Callers must preserve
// such a container: unknown ownership is not evidence that it is orphaned.
func postgresOwnerState(labels map[string]string) (alive bool, known bool) {
	pid, err := strconv.Atoi(labels[postgresOwnerPIDLabel])
	if err != nil || pid <= 0 {
		return false, false
	}

	expectedStartID := labels[postgresOwnerStartLabel]
	if expectedStartID == "" {
		return false, false
	}

	currentStartID, state := postgresProcessIdentity(pid)
	if currentStartID != "" {
		return state != "Z" && currentStartID == expectedStartID, true
	}

	// procfs was unavailable for this PID. Signal 0 checks existence and
	// permission without delivering a signal. Without a comparable start ID,
	// an existing process is considered alive conservatively.
	process, err := os.FindProcess(pid)
	if err != nil {
		return false, true
	}
	if err := process.Signal(syscall.Signal(0)); err != nil {
		if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
			return false, true
		}
		// The process may exist but be inaccessible to this user. Preserve it
		// because ownership was not disproved.
		return true, true
	}
	return true, true
}

// postgresProcessIdentity returns the kernel start-time identity and state of
// a process. Together they distinguish the original owner from an unrelated
// process that happens to reuse the same PID after it exits.
func postgresProcessIdentity(pid int) (startID string, state string) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", ""
	}

	// The executable name is parenthesized and may itself contain spaces or
	// closing parentheses, so split after its final closing parenthesis.
	stat := string(data)
	commandEnd := strings.LastIndex(stat, ")")
	if commandEnd < 0 || commandEnd+1 >= len(stat) {
		return "", ""
	}

	fields := strings.Fields(stat[commandEnd+1:])
	if len(fields) == 0 {
		return "", ""
	}
	state = fields[0]
	// fields[0] is field 3 (state); starttime is field 22.
	const startTimeIndex = 22 - 3
	if len(fields) <= startTimeIndex {
		return "", state
	}
	return fields[startTimeIndex], state
}

func postgresProcessStartID(pid int) string {
	startID, _ := postgresProcessIdentity(pid)
	return startID
}
