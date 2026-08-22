//go:build linux

package proctable

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/runtime"
)

// ScanBySessionID returns live agent root processes whose environment carries
// GC_SESSION_ID equal to id. Empty id returns all roots with any GC_SESSION_ID.
func ScanBySessionID(id string) ([]runtime.LiveRuntime, error) {
	if err := liveScanGuard(); err != nil {
		return []runtime.LiveRuntime{}, err
	}
	return scanWithRoot(scanRoot, id)
}

// ScanBySessionIDSince scans for an exact session incarnation. Inspection
// failures from processes proven outside the incarnation's reachable scope —
// predating incarnationStartedAt, or rooted in a foreign pre-incarnation
// lineage — do not make absence incomplete; those processes cannot belong to
// that incarnation. A nil error therefore proves absence within that scope,
// not across the whole host.
func ScanBySessionIDSince(id string, incarnationStartedAt time.Time) ([]runtime.LiveRuntime, error) {
	return ScanBySessionIDSinceInScope(id, incarnationStartedAt, SessionScope{})
}

// ScanBySessionIDSinceInScope is ScanBySessionIDSince with caller-established
// scope facts. A nil error proves absence within the session's reachable
// scope — the pane lineage the runtime layer spawned for this incarnation plus
// every owned process no proof could exclude — NOT absence across the whole
// host: unreadable owned processes provably outside that scope (predating the
// incarnation, living in another live pane's spawn scope, or rooted in a
// foreign pre-incarnation lineage) do not cost the sweep its completeness
// (ga-lp5w6). Processes the kernel has already killed leave the domain before
// any of that: a corpse cannot be a living runtime (ga-f7v2ft.194).
func ScanBySessionIDSinceInScope(id string, incarnationStartedAt time.Time, scope SessionScope) ([]runtime.LiveRuntime, error) {
	if err := liveScanGuard(); err != nil {
		return []runtime.LiveRuntime{}, err
	}
	return scanWithRootSinceInScope(scanRoot, id, incarnationStartedAt, scope)
}

// IsScanRoot reports whether pid is outside its GC_SESSION_ID parent's
// envelope and should be treated as an agent root.
func IsScanRoot(pid int) bool {
	if err := liveScanGuard(); err != nil {
		return false
	}
	if pid == 1 {
		return true
	}
	if pid <= 0 {
		return false
	}
	if pid == os.Getpid() {
		return false
	}
	env, err := parseEnvironFile(filepath.Join(scanRoot, strconv.Itoa(pid), "environ"))
	if err != nil || len(env) == 0 {
		return false
	}
	sessionID := env["GC_SESSION_ID"]
	if sessionID == "" {
		return false
	}
	isRoot, err := isRootWithSessionID(scanRoot, pid, sessionID)
	return err == nil && isRoot
}

func scanWithRoot(root, id string) ([]runtime.LiveRuntime, error) {
	return scanWithRootSince(root, id, time.Time{})
}

func scanWithRootSince(root, id string, incarnationStartedAt time.Time) ([]runtime.LiveRuntime, error) {
	return scanWithRootSinceInScope(root, id, incarnationStartedAt, SessionScope{})
}

func scanWithRootSinceInScope(root, id string, incarnationStartedAt time.Time, scope SessionScope) ([]runtime.LiveRuntime, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return []runtime.LiveRuntime{}, fmt.Errorf("enumerating %s: %w", root, err)
	}

	var (
		out     []runtime.LiveRuntime
		residue scanResidue
	)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 1 {
			continue
		}
		owned, err := processOwnedByUID(root, pid, os.Geteuid())
		if err != nil {
			if irrelevant, proofErr := processPredatesIncarnation(root, pid, incarnationStartedAt); irrelevant {
				continue
			} else if proofErr != nil {
				residue.add(fmt.Errorf("proving age for pid %d: %w", pid, proofErr))
			}
			residue.add(fmt.Errorf("reading owner for pid %d: %w", pid, err))
			continue
		}
		if !owned {
			continue
		}
		if processProvenKernelDead(root, pid) {
			// A zombie (state Z) or dead (X) process has already been killed:
			// its address space is gone, no code will ever run under that pid
			// again, and it therefore cannot be this session's living runtime.
			// It owes the sweep no proof, readable or not (ga-f7v2ft.194).
			residue.excludeKernelDead()
			continue
		}
		env, err := parseEnvironFile(filepath.Join(root, entry.Name(), "environ"))
		if err != nil {
			if irrelevant, proofErr := processPredatesIncarnation(root, pid, incarnationStartedAt); irrelevant {
				continue
			} else if proofErr != nil {
				residue.add(fmt.Errorf("proving age for pid %d: %w", pid, proofErr))
			}
			if irrelevant, proofErr := unreadableProcessProvenOutsideIncarnation(
				root,
				pid,
				id,
				incarnationStartedAt,
			); irrelevant {
				continue
			} else if proofErr != nil {
				residue.add(fmt.Errorf("proving tmux parent for pid %d: %w", pid, proofErr))
			}
			if irrelevant, proofErr := unreadableProcessProvenInForeignLivePaneScope(
				root,
				pid,
				id,
				incarnationStartedAt,
				scope,
			); irrelevant {
				continue
			} else if proofErr != nil {
				residue.add(fmt.Errorf("proving live pane scope for pid %d: %w", pid, proofErr))
			}
			if irrelevant, proofErr := unreadableProcessProvenForeignLineage(
				root,
				pid,
				incarnationStartedAt,
			); irrelevant {
				continue
			} else if proofErr != nil {
				residue.add(fmt.Errorf("proving foreign lineage for pid %d: %w", pid, proofErr))
			}
			residue.add(fmt.Errorf("reading environ for pid %d: %w", pid, err))
			continue
		}
		if root == "/proc" && pid == os.Getpid() {
			env = mergeCurrentEnv(env)
		}
		if len(env) == 0 {
			continue
		}
		sessionID := env["GC_SESSION_ID"]
		if sessionID == "" {
			continue
		}
		if id != "" && sessionID != id {
			continue
		}
		rootProcess, err := isRootWithSessionID(root, pid, sessionID)
		if err != nil {
			residue.add(fmt.Errorf("checking root for pid %d: %w", pid, err))
			continue
		}
		if !rootProcess {
			continue
		}
		epoch, _ := strconv.Atoi(env["GC_RUNTIME_EPOCH"])
		city := env["GC_CITY_PATH"]
		if city == "" {
			city = env["GC_CITY"]
		}
		out = append(out, runtime.LiveRuntime{
			SessionID: sessionID,
			City:      city,
			Epoch:     epoch,
			PID:       pid,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].PID < out[j].PID
	})
	if out == nil {
		out = []runtime.LiveRuntime{}
	}
	return out, residue.err()
}

// scanResidue accumulates the per-process inspection failures a sweep could
// not resolve, so it can report them as one line instead of one line per
// process — 513k of a production supervisor log's 637k lines, at ~253 per
// sweep (gastownhall/gascity ga-f7v2ft.172).
//
// Only the reporting is collapsible. The residue itself cannot be pruned by
// skipping the reads, because every process that lands here is one we are
// already entitled to care about: processOwnedByUID has passed, so the failures
// left are our own processes whose environ the kernel still withholds. It
// guards /proc/<pid>/environ with ptrace_may_access, not file permissions, so a
// non-dumpable agent and a sudo child both stay unreadable to the uid that owns
// them. None may be assumed absent — a sudo child can outlive the runtime
// that spawned it and keep carrying its GC_SESSION_ID — but some can be PROVEN
// outside the incarnation's reachable scope: predating it, living in another
// live pane's spawn scope, or rooted in a foreign pre-incarnation lineage
// (the unreadableProcessProven* adjudications, ga-lp5w6). Kernel-dead
// processes never reach here at all: processProvenKernelDead drops them
// before the environ read, counted rather than itemized. What remains in the
// residue is the genuinely undecidable set, each failure still costs the sweep
// its proof of absence, and err() keeps a non-nil verdict whenever one
// remains — so a nil verdict means absence proven within the session's
// reachable scope, not inspected across the host.
type scanResidue struct {
	errs              []error
	excludedDeadCount int
}

func (r *scanResidue) add(err error) {
	if err != nil {
		r.errs = append(r.errs, err)
	}
}

// excludeKernelDead records a process the kernel already killed. Those are
// dropped from the completeness domain rather than proven, so they carry no
// error — but a host running hundreds of them is worth seeing, and the count
// rides the existing summary instead of a line per corpse.
func (r *scanResidue) excludeKernelDead() {
	r.excludedDeadCount++
}

// err returns nil only when every process was inspected or proven irrelevant,
// so callers keep reading a nil error as their proof that absence is complete.
func (r *scanResidue) err() error {
	var err error
	switch len(r.errs) {
	case 0:
		return nil
	case 1:
		err = r.errs[0]
	default:
		err = &inspectionResidueError{errs: r.errs}
	}
	if r.excludedDeadCount > 0 {
		return fmt.Errorf("%w (excluded %d kernel-dead processes)", err, r.excludedDeadCount)
	}
	return err
}

// inspectionResidueError renders a sweep's uninspectable processes as a count
// and one example while keeping each underlying error reachable through
// [errors.Is] and [errors.As].
type inspectionResidueError struct {
	errs []error
}

func (e *inspectionResidueError) Error() string {
	return fmt.Sprintf("%d processes could not be inspected (first: %v)", len(e.errs), e.errs[0])
}

func (e *inspectionResidueError) Unwrap() []error { return e.errs }

const (
	linuxUserHZ                  = 100
	linuxProcessStartUncertainty = time.Second + time.Second/linuxUserHZ
)

// processProvenKernelDead reports whether /proc/<pid>/stat proves the process
// already terminated. It is a proof, not a query: a stat file that is missing,
// unreadable or malformed grants no exclusion and returns false, leaving the
// process on its ordinary fail-closed path.
//
// The state field is world-readable and needs no ptrace access, which is why
// it can be consulted before environ. A zombie's environ, by contrast, is
// denied even to its own uid (measured on the incident host: EACCES on
// /proc/<zombie>/environ while status still reported our uid), so before this
// check every unreaped process cost the sweep its completeness — and orphaned
// zombies re-parent to init, the one lineage shape both ga-lp5w6 scope proofs
// decline. 522 of them on one supervisor host held every acked drain, and
// every drain's pool seat, forever (ga-f7v2ft.194).
func processProvenKernelDead(root string, pid int) bool {
	stat, exists, err := readProcessStat(root, pid)
	if err != nil || !exists {
		return false
	}
	return processStateIsKernelDead(stat.State)
}

// processStateIsKernelDead reports whether a /proc/<pid>/stat process state is
// terminal: Z is a zombie awaiting its parent's wait(), X (x on older kernels)
// is dead. Every other state — including the uninterruptible D and the stopped
// T/t — describes a process that can still run.
func processStateIsKernelDead(state string) bool {
	switch state {
	case "Z", "X", "x":
		return true
	default:
		return false
	}
}

func processPredatesIncarnation(root string, pid int, incarnationStartedAt time.Time) (bool, error) {
	if incarnationStartedAt.IsZero() || incarnationStartedAt.After(time.Now()) {
		return false, nil
	}
	bootedAt, err := readBootTime(root)
	if err != nil {
		return false, err
	}
	startedAt, exists, err := readProcessStartTime(root, pid, bootedAt)
	if err != nil {
		return false, err
	}
	if !exists {
		return true, nil
	}
	return processDefinitelyPredatesIncarnation(startedAt, incarnationStartedAt), nil
}

func processDefinitelyPredatesIncarnation(startedAt, incarnationStartedAt time.Time) bool {
	// /proc/stat exposes btime only to whole seconds and /proc/<pid>/stat
	// exposes start time in USER_HZ ticks. Require the process to precede the
	// boundary by more than both quantization errors before excluding it.
	return startedAt.Add(linuxProcessStartUncertainty).Before(incarnationStartedAt)
}

type processIdentity struct {
	PID        int
	PPID       int
	State      string
	StartTicks uint64
	Cgroup     string
}

func unreadableProcessProvenOutsideIncarnation(
	root string,
	pid int,
	targetSessionID string,
	incarnationStartedAt time.Time,
) (bool, error) {
	if targetSessionID == "" ||
		incarnationStartedAt.IsZero() ||
		incarnationStartedAt.After(time.Now()) {
		return false, nil
	}

	bootedAt, err := readBootTime(root)
	if err != nil {
		return false, err
	}
	candidateBefore, exists, err := readProcessIdentity(root, pid)
	if err != nil || !exists {
		return false, err
	}
	if processDefinitelyPredatesIncarnation(
		processStartedAt(bootedAt, candidateBefore.StartTicks),
		incarnationStartedAt,
	) ||
		candidateBefore.PPID <= 1 ||
		!isUniqueTmuxSpawnScope(candidateBefore.Cgroup) {
		return false, nil
	}

	parentBefore, exists, err := readProcessIdentity(root, candidateBefore.PPID)
	if err != nil || !exists {
		return false, err
	}
	// The parent's environ is this proof's evidence, so a parent the kernel
	// already killed decides nothing: a corpse has no address space, and its
	// environ reads empty (or EACCES) whatever it once carried.
	if parentBefore.Cgroup != candidateBefore.Cgroup ||
		processStateIsKernelDead(parentBefore.State) ||
		!processDefinitelyPredatesIncarnation(
			processStartedAt(bootedAt, parentBefore.StartTicks),
			incarnationStartedAt,
		) {
		return false, nil
	}

	parentEnv, err := parseEnvironFile(
		filepath.Join(root, strconv.Itoa(parentBefore.PID), "environ"),
	)
	if err != nil || parentEnv == nil {
		return false, err
	}
	if parentEnv["GC_SESSION_ID"] == targetSessionID {
		return false, nil
	}

	parentAfter, exists, err := readProcessIdentity(root, parentBefore.PID)
	if err != nil || !exists {
		return false, err
	}
	candidateAfter, exists, err := readProcessIdentity(root, candidateBefore.PID)
	if err != nil || !exists {
		return false, err
	}
	if parentAfter.PID != parentBefore.PID ||
		parentAfter.StartTicks != parentBefore.StartTicks ||
		parentAfter.Cgroup != parentBefore.Cgroup ||
		candidateAfter != candidateBefore {
		return false, nil
	}
	return true, nil
}

// maxUnreadableProofChainHops bounds the parent-chain walks below. Real pane
// and login lineages are a handful of processes deep; a chain longer than this
// is either a pathological tree or a ppid cycle, and both must fail closed.
const maxUnreadableProofChainHops = 32

// unreadableProcessProvenInForeignLivePaneScope adjudicates an unreadable
// owned process inside a unique tmux pane spawn scope by following its parent
// chain to the scope's exit — the live process that spawned the pane. If that
// spawner predates the incarnation and its readable environment does not carry
// the target session ID, the pane belongs to some other lineage: a unique
// spawn scope holds exactly one pane's subtree, membership only changes by an
// explicit privileged cgroup write, and the target's own incarnation-spawned
// pane would hang off the same spawner only when the caller-supplied license —
// a same-generation COMPLETE tmux observation proving the target session
// absent — could not have been granted. Every undecidable link (an ancestor
// re-parented to init, an unreadable, kernel-dead or post-incarnation spawner,
// an unstable chain) declines, leaving the process in the residue. Dead
// intermediate links are fine: a zombie's ppid still records who spawned it,
// so the walk passes through it to the scope's live exit.
func unreadableProcessProvenInForeignLivePaneScope(
	root string,
	pid int,
	targetSessionID string,
	incarnationStartedAt time.Time,
	scope SessionScope,
) (bool, error) {
	if !scope.TmuxSessionProvenAbsent ||
		targetSessionID == "" ||
		incarnationStartedAt.IsZero() ||
		incarnationStartedAt.After(time.Now()) {
		return false, nil
	}

	bootedAt, err := readBootTime(root)
	if err != nil {
		return false, err
	}
	candidateBefore, exists, err := readProcessIdentity(root, pid)
	if err != nil || !exists {
		return false, err
	}
	if !isUniqueTmuxSpawnScope(candidateBefore.Cgroup) {
		return false, nil
	}

	cur := candidateBefore
	for range maxUnreadableProofChainHops {
		if cur.PPID <= 1 {
			// A chain that exits to init is the re-parented orphan shape — the
			// scope's pane is gone and nothing proves whose pane it was.
			return false, nil
		}
		parent, exists, err := readProcessIdentity(root, cur.PPID)
		if err != nil || !exists {
			return false, err
		}
		if parent.Cgroup == candidateBefore.Cgroup {
			cur = parent
			continue
		}
		// parent is the scope-exit ancestor: the live spawner of this pane.
		if processStateIsKernelDead(parent.State) {
			return false, nil
		}
		if !processDefinitelyPredatesIncarnation(
			processStartedAt(bootedAt, parent.StartTicks),
			incarnationStartedAt,
		) {
			return false, nil
		}
		parentEnv, err := parseEnvironFile(
			filepath.Join(root, strconv.Itoa(parent.PID), "environ"),
		)
		if err != nil || parentEnv == nil {
			return false, err
		}
		if parentEnv["GC_SESSION_ID"] == targetSessionID {
			return false, nil
		}
		parentAfter, exists, err := readProcessIdentity(root, parent.PID)
		if err != nil || !exists {
			return false, err
		}
		candidateAfter, exists, err := readProcessIdentity(root, pid)
		if err != nil || !exists {
			return false, err
		}
		if parentAfter != parent || candidateAfter != candidateBefore {
			return false, nil
		}
		return true, nil
	}
	return false, nil
}

// unreadableProcessProvenForeignLineage adjudicates an unreadable owned
// process by its parent chain alone: if every ancestor up to the first
// pre-incarnation one runs as a foreign real uid — and the chain never exits
// to init — the lineage is rooted in a process that existed before this
// session and belongs to another uid domain (an sshd login tree, a container
// supervisor). Nothing the session spawned can be rooted there: every process
// in the session's own tree descends from the incarnation root, so its chain
// reaches a same-uid ancestor (declined) or init via re-parenting (declined)
// before any foreign pre-incarnation process.
func unreadableProcessProvenForeignLineage(
	root string,
	pid int,
	incarnationStartedAt time.Time,
) (bool, error) {
	if incarnationStartedAt.IsZero() || incarnationStartedAt.After(time.Now()) {
		return false, nil
	}

	bootedAt, err := readBootTime(root)
	if err != nil {
		return false, err
	}
	candidateBefore, exists, err := readProcessStat(root, pid)
	if err != nil || !exists {
		return false, err
	}

	uid := os.Geteuid()
	cur := candidateBefore
	for range maxUnreadableProofChainHops {
		if cur.PPID <= 1 {
			// Parented by init: the re-parenting target for surviving session
			// processes. Never decidable from lineage.
			return false, nil
		}
		owned, err := processOwnedByUID(root, cur.PPID, uid)
		if err != nil {
			return false, err
		}
		if owned {
			// A same-uid ancestor could be the session's own sudo tree.
			return false, nil
		}
		parent, exists, err := readProcessStat(root, cur.PPID)
		if err != nil || !exists {
			return false, err
		}
		if processDefinitelyPredatesIncarnation(
			processStartedAt(bootedAt, parent.StartTicks),
			incarnationStartedAt,
		) {
			candidateAfter, exists, err := readProcessStat(root, pid)
			if err != nil || !exists {
				return false, err
			}
			if candidateAfter != candidateBefore {
				return false, nil
			}
			return true, nil
		}
		cur = parent
	}
	return false, nil
}

func readProcessIdentity(root string, pid int) (processIdentity, bool, error) {
	stat, exists, err := readProcessStat(root, pid)
	if err != nil || !exists {
		return processIdentity{}, exists, err
	}
	cgroup, exists, err := readProcessCgroup(root, pid)
	if err != nil || !exists {
		return processIdentity{}, exists, err
	}
	return processIdentity{
		PID:        stat.PID,
		PPID:       stat.PPID,
		State:      stat.State,
		StartTicks: stat.StartTicks,
		Cgroup:     cgroup,
	}, true, nil
}

func readProcessCgroup(root string, pid int) (string, bool, error) {
	path := filepath.Join(root, strconv.Itoa(pid), "cgroup")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	var cgroup string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, ":", 3)
		if len(fields) != 3 || fields[2] == "" || cgroup != "" {
			return "", false, fmt.Errorf("malformed cgroup file %s", path)
		}
		cgroup = filepath.Clean(fields[2])
	}
	if cgroup == "" {
		return "", false, fmt.Errorf("missing cgroup path in %s", path)
	}
	return cgroup, true, nil
}

func isUniqueTmuxSpawnScope(cgroup string) bool {
	if cgroup == "" || cgroup == "." || cgroup == "/" {
		return false
	}
	leaf := filepath.Base(cgroup)
	const (
		prefix = "tmux-spawn-"
		suffix = ".scope"
	)
	return strings.HasPrefix(leaf, prefix) &&
		strings.HasSuffix(leaf, suffix) &&
		len(leaf) > len(prefix)+len(suffix)
}

func readBootTime(root string) (time.Time, error) {
	data, err := os.ReadFile(filepath.Join(root, "stat"))
	if err != nil {
		return time.Time{}, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "btime" {
			continue
		}
		seconds, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return time.Time{}, fmt.Errorf("parsing btime %q: %w", fields[1], err)
		}
		return time.Unix(seconds, 0).UTC(), nil
	}
	return time.Time{}, fmt.Errorf("missing btime field")
}

type processStat struct {
	PID   int
	PPID  int
	State string
	// StartTicks is the process start time in USER_HZ ticks since boot.
	StartTicks uint64
}

func readProcessStat(root string, pid int) (processStat, bool, error) {
	path := filepath.Join(root, strconv.Itoa(pid), "stat")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return processStat{}, false, nil
		}
		return processStat{}, false, err
	}
	text := string(data)
	openParen := strings.Index(text, "(")
	closeParen := strings.LastIndex(text, ")")
	if openParen <= 0 || closeParen < openParen || closeParen+1 >= len(text) {
		return processStat{}, false, fmt.Errorf("malformed stat file %s", path)
	}
	observedPID, err := strconv.Atoi(strings.TrimSpace(text[:openParen]))
	if err != nil || observedPID != pid {
		return processStat{}, false, fmt.Errorf("invalid pid in stat file %s", path)
	}
	// comm is arbitrary process-controlled text that may contain spaces and
	// parens, so the fields after it can only be found from the LAST ')'. From
	// there fields[0] is the state, fields[1] the ppid, fields[19] the start
	// time (stat fields 3, 4 and 22).
	fields := strings.Fields(text[closeParen+1:])
	const starttimeIndexAfterComm = 19
	if len(fields) <= starttimeIndexAfterComm {
		return processStat{}, false, fmt.Errorf("malformed stat file %s", path)
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return processStat{}, false, fmt.Errorf("parsing ppid from %s: %w", path, err)
	}
	startTicks, err := strconv.ParseUint(fields[starttimeIndexAfterComm], 10, 64)
	if err != nil {
		return processStat{}, false, fmt.Errorf("parsing start time from %s: %w", path, err)
	}
	return processStat{
		PID:        observedPID,
		PPID:       ppid,
		State:      fields[0],
		StartTicks: startTicks,
	}, true, nil
}

func readProcessStartTime(root string, pid int, bootedAt time.Time) (time.Time, bool, error) {
	stat, exists, err := readProcessStat(root, pid)
	if err != nil || !exists {
		return time.Time{}, exists, err
	}
	return processStartedAt(bootedAt, stat.StartTicks), true, nil
}

func processStartedAt(bootedAt time.Time, startTicks uint64) time.Time {
	wholeSeconds := startTicks / linuxUserHZ
	remainderTicks := startTicks % linuxUserHZ
	return bootedAt.Add(
		time.Duration(wholeSeconds)*time.Second +
			time.Duration(remainderTicks)*(time.Second/linuxUserHZ),
	)
}

func processOwnedByUID(root string, pid, uid int) (bool, error) {
	data, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "status"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "Uid:") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "Uid:"))
		if len(fields) == 0 {
			break
		}
		observed, err := strconv.Atoi(fields[0])
		if err != nil {
			break
		}
		return observed == uid, nil
	}
	return false, fmt.Errorf("missing valid Uid field")
}

func mergeCurrentEnv(env map[string]string) map[string]string {
	if env == nil {
		env = make(map[string]string)
	}
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			continue
		}
		env[key] = value
	}
	return env
}

func parseEnvironFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	env := make(map[string]string)
	for _, entry := range strings.Split(string(data), "\x00") {
		if entry == "" {
			continue
		}
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			continue
		}
		env[key] = value
	}
	return env, nil
}

func isRootWithSessionID(root string, pid int, sessionID string) (bool, error) {
	ppid, ok, err := readParentPID(filepath.Join(root, strconv.Itoa(pid), "stat"))
	if err != nil {
		return false, err
	}
	if !ok {
		// stat vanished between environ read and here; process died in the race
		// window — skip rather than misreport it as a root.
		return false, nil
	}
	if ppid <= 1 {
		return true, nil
	}
	parentEnv, err := parseEnvironFile(filepath.Join(root, strconv.Itoa(ppid), "environ"))
	if err != nil {
		return false, err
	}
	if parentEnv["GC_SESSION_ID"] == sessionID && isInfrastructureParent(root, ppid) {
		return true, nil
	}
	return parentEnv["GC_SESSION_ID"] != sessionID, nil
}

func isInfrastructureParent(root string, pid int) bool {
	data, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "comm"))
	if err != nil {
		return false
	}
	command := strings.ToLower(strings.TrimSpace(string(data)))
	return strings.Contains(command, "tmux")
}

func readParentPID(path string) (int, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, false, nil
		}
		return 0, false, err
	}
	text := string(data)
	closeParen := strings.LastIndex(text, ")")
	if closeParen < 0 || closeParen+1 >= len(text) {
		return 0, false, fmt.Errorf("malformed stat file %s", path)
	}
	fields := strings.Fields(text[closeParen+1:])
	if len(fields) < 2 {
		return 0, false, fmt.Errorf("malformed stat file %s", path)
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, false, fmt.Errorf("parsing ppid from %s: %w", path, err)
	}
	return ppid, true, nil
}
