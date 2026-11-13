package testdb

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestPostgresOwnerLabelsRecordTheCurrentProcess(t *testing.T) {
	labels := postgresContainerLabels("ovumcy_owner_test")
	if len(labels) != 8 {
		t.Fatalf("expected four label pairs, got %d arguments", len(labels))
	}

	values := make(map[string]string)
	for index := 0; index < len(labels); index += 2 {
		if labels[index] != "--label" {
			t.Fatalf("argument %d: expected --label, got %q", index, labels[index])
		}
		key, value, found := strings.Cut(labels[index+1], "=")
		if !found {
			t.Fatalf("label %q must contain a value", labels[index+1])
		}
		values[key] = value
	}

	if values[postgresManagedLabel] != "true" {
		t.Fatalf("managed label = %q, want true", values[postgresManagedLabel])
	}
	if values[postgresDatabaseNameLabel] != "ovumcy_owner_test" {
		t.Fatalf("database label = %q, want ovumcy_owner_test", values[postgresDatabaseNameLabel])
	}

	pid, err := strconv.Atoi(values[postgresOwnerPIDLabel])
	if err != nil || pid != os.Getpid() {
		t.Fatalf("owner PID label = %q, want %d", values[postgresOwnerPIDLabel], os.Getpid())
	}
	startID := postgresProcessStartID(os.Getpid())
	if startID == "" {
		t.Fatal("current process has no start identity")
	}
	if values[postgresOwnerStartLabel] != startID {
		t.Fatalf("owner start label = %q, want %q", values[postgresOwnerStartLabel], startID)
	}
}

func TestPostgresOwnerStatePreservesLiveAndUnknownOwners(t *testing.T) {
	startID := postgresProcessStartID(os.Getpid())
	if startID == "" {
		t.Fatal("current process has no start identity")
	}

	live := map[string]string{
		postgresOwnerPIDLabel:   strconv.Itoa(os.Getpid()),
		postgresOwnerStartLabel: startID,
	}
	alive, known := postgresOwnerState(live)
	if !known || !alive {
		t.Fatalf("live owner state = (%v, %v), want (true, true)", alive, known)
	}

	unknown := map[string]string{
		postgresOwnerPIDLabel: "not-a-pid",
	}
	alive, known = postgresOwnerState(unknown)
	if known || alive {
		t.Fatalf("malformed owner state = (%v, %v), want (false, false)", alive, known)
	}
}

func TestPostgresOwnerStateRecognizesAStaleOwner(t *testing.T) {
	startID := postgresProcessStartID(os.Getpid())
	if startID == "" {
		t.Fatal("current process has no start identity")
	}

	stale := map[string]string{
		postgresOwnerPIDLabel:   strconv.Itoa(os.Getpid()),
		postgresOwnerStartLabel: startID + "-different-process",
	}
	alive, known := postgresOwnerState(stale)
	if !known || alive {
		t.Fatalf("stale owner state = (%v, %v), want (false, true)", alive, known)
	}
}

func TestCleanupStalePostgresContainersRemovesOnlyDeadOwners(t *testing.T) {
	original := runDockerCommandWithError
	t.Cleanup(func() { runDockerCommandWithError = original })

	liveStartID := postgresProcessStartID(os.Getpid())
	if liveStartID == "" {
		t.Fatal("current process has no start identity")
	}

	liveLabels := map[string]string{
		postgresOwnerPIDLabel:   strconv.Itoa(os.Getpid()),
		postgresOwnerStartLabel: liveStartID,
	}
	staleLabels := map[string]string{
		postgresOwnerPIDLabel:   strconv.Itoa(os.Getpid()),
		postgresOwnerStartLabel: liveStartID + "-different-process",
	}

	removed := make([]string, 0)
	runDockerCommandWithError = func(args ...string) (string, error) {
		switch {
		case len(args) == 4 && args[0] == "ps":
			return "live stale", nil
		case len(args) == 3 && args[0] == "inspect" && args[2] == "live":
			return `{"` + postgresOwnerPIDLabel + `":"` + liveLabels[postgresOwnerPIDLabel] + `","` + postgresOwnerStartLabel + `":"` + liveLabels[postgresOwnerStartLabel] + `"}`, nil
		case len(args) == 3 && args[0] == "inspect" && args[2] == "stale":
			return `{"` + postgresOwnerPIDLabel + `":"` + staleLabels[postgresOwnerPIDLabel] + `","` + postgresOwnerStartLabel + `":"` + staleLabels[postgresOwnerStartLabel] + `"}`, nil
		case len(args) == 4 && args[0] == "rm" && args[1] == "-f" && args[2] == "-v":
			removed = append(removed, args[3])
			return "", nil
		default:
			t.Fatalf("unexpected docker command: %v", args)
			return "", nil
		}
	}

	cleanupStalePostgresContainers(t)

	if len(removed) != 1 || removed[0] != "stale" {
		t.Fatalf("removed containers = %v, want [stale]", removed)
	}
}
