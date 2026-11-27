package machine

import "testing"

// kill through the real kernel instructions (F2-02, R10): page_release runs
// before any lifecycle effect, successful termination keeps its outcomes, and
// malformed metadata stops at kill_cleanup_panic without lifecycle writes.

const killTarget = 1

func newKillFixture(t *testing.T) *kernelFixture {
	t.Helper()
	f := newKernelFixture(t)
	f.identityPCBPages(0) // status delivery translates through the parent's table
	f.put(uint32(GlobalAddress(GlobalMemorySizeOffset)), testMemorySize)
	f.put(uint32(GlobalAddress(GlobalMaxProcessesOffset)), 5)
	f.put(uint32(GlobalAddress(GlobalRunningPIDOffset)), killTarget)
	prepareKillCleanup(f, killTarget)
	for _, pid := range []uint32{2, 3, 4} {
		mapPCBHeader(f, pid)
	}
	return f
}

func setFamily(f *kernelFixture, pid, parent, child, prev, next, flags uint32) {
	p := pcbAddr(pid)
	f.put(p, parent)
	f.put(p+4, child)
	f.put(p+8, prev)
	f.put(p+12, next)
	f.put(p+PCBFlagsOffset, flags)
}

const none = ^uint32(0)

// waitingParentScene: parent 0 waits; its children are 3 -> 1 -> 2; the
// target 1 has child 4 and exits with status 42. Returns the status word's
// physical address.
func waitingParentScene(f *kernelFixture) uint32 {
	setFamily(f, 0, none, 3, none, none, 5) // mapped, waiting
	setFamily(f, 3, 0, none, none, 1, 9)
	setFamily(f, killTarget, 0, 4, 3, 2, 9)
	setFamily(f, 2, 0, none, 1, none, 9)
	setFamily(f, 4, killTarget, none, none, none, 9)
	f.put(pcbAddr(0)+16, 0x1008)               // parent status address
	f.put(pcbAddr(0)+PCBW9Offset, 0x11111111)  // parent saved W9
	f.put(pcbAddr(killTarget)+PCBW9Offset, 42) // target exit status
	statusFrame := f.next
	f.next += pageSize
	f.put(pteAddr(0, 1), isPteMappedMask|statusFrame/pageSize)
	return statusFrame + 8
}

type killRun struct {
	before []byte
	uptr   uint32
	stores []memAccess
}

func runKill(t *testing.T, f *kernelFixture, until string, validPages int) killRun {
	t.Helper()
	run := killRun{before: snapshotMemory(f), uptr: f.m.registers[uptr]}
	f.m.registers[w9] = killTarget
	runTraced(f, "kill", until, cleanupBudget(validPages), func(a memAccess) {
		if a.store {
			run.stores = append(run.stores, a)
		}
	})
	return run
}

// cleanupAllowed lists the words page_release may change for these pages.
func cleanupAllowed(pages []userPage) map[uint32]bool {
	allowed := map[uint32]bool{pagesUsedAddr(killTarget): true}
	stackWords(allowed)
	for _, p := range pages {
		allowed[pteAddr(killTarget, p.index)] = true
		allowed[bitmapWordAddr(p.frame)] = true
	}
	return allowed
}

func isCleanupStore(a memAccess) bool {
	table := pcbAddr(killTarget) + pcbTableOffset
	return !a.user && (a.addr == pagesUsedAddr(killTarget) ||
		(a.addr >= table && a.addr < table+userTableEntries*4) ||
		(a.addr >= uint32(BitmapStart) && a.addr < uint32(BitmapEnd)))
}

func isStackStore(a memAccess) bool {
	return !a.user && a.addr >= killCleanupStack && a.addr < killCleanupStack+pageSize
}

// assertCleanupFirst checks that every release write precedes every
// lifecycle write and happens under the caller's original UPTR.
func assertCleanupFirst(t *testing.T, run killRun) {
	t.Helper()
	lastCleanup, firstLifecycle := -1, -1
	for _, s := range run.stores {
		switch {
		case isCleanupStore(s):
			lastCleanup = s.step
			if s.uptr != run.uptr {
				t.Errorf("cleanup store at step %d under UPTR %#x, want %#x", s.step, s.uptr, run.uptr)
			}
		case isStackStore(s):
		default:
			if firstLifecycle < 0 {
				firstLifecycle = s.step
			}
		}
	}
	if lastCleanup < 0 || firstLifecycle < 0 {
		t.Fatalf("missing cleanup (%d) or lifecycle (%d) writes", lastCleanup, firstLifecycle)
	}
	if lastCleanup > firstLifecycle {
		t.Fatalf("cleanup store at step %d after lifecycle store at step %d", lastCleanup, firstLifecycle)
	}
}

func assertTargetReleased(t *testing.T, f *kernelFixture, pages []userPage) {
	t.Helper()
	for _, p := range pages {
		if f.get(pteAddr(killTarget, p.index)) != 0 || frameBit(f, p.frame) != 0 {
			t.Errorf("page %#x not released", p.index)
		}
	}
	if got := f.get(pagesUsedAddr(killTarget)); got != 0 {
		t.Errorf("pages_used = %d, want 0", got)
	}
}

func assertOrphaned(t *testing.T, f *kernelFixture, pid uint32) {
	t.Helper()
	p := pcbAddr(pid)
	if f.get(p) != none || f.get(p+8) != none || f.get(p+12) != none {
		t.Errorf("PID %d not orphaned: parent=%#x prev=%#x next=%#x", pid, f.get(p), f.get(p+8), f.get(p+12))
	}
}

var killPages = []userPage{{0, 0x40C00}, {0xBFFFF, 0x40C01}}

// A17: waiting parent, siblings on both sides and a child.
func TestKillCleanupWaitingParent(t *testing.T) {
	t.Parallel()
	f := newKillFixture(t)
	status := waitingParentScene(f)
	for _, p := range killPages {
		addUserPage(f, killTarget, p)
	}
	run := runKill(t, f, "schedule", len(killPages))

	assertTargetReleased(t, f, killPages)
	assertCleanupFirst(t, run)
	if f.get(pcbAddr(0)+PCBFlagsOffset) != 9 || f.get(pcbAddr(0)+PCBW9Offset) != killTarget {
		t.Errorf("parent flags=%#x W9=%#x, want ready and child PID", f.get(pcbAddr(0)+PCBFlagsOffset), f.get(pcbAddr(0)+PCBW9Offset))
	}
	if got := f.m.memory[status]; got != 42 {
		t.Errorf("delivered exit status %d, want 42", got)
	}
	if f.get(pcbAddr(0)+4) != 3 || f.get(pcbAddr(3)+12) != 2 || f.get(pcbAddr(2)+8) != 3 {
		t.Error("sibling links not repaired around the target")
	}
	if f.get(pcbAddr(killTarget)+PCBFlagsOffset) != 8 {
		t.Errorf("target flags %#x, want slot unmapped", f.get(pcbAddr(killTarget)+PCBFlagsOffset))
	}
	assertOrphaned(t, f, 4)

	allowed := cleanupAllowed(killPages)
	for _, a := range []uint32{pcbAddr(0) + 4, pcbAddr(0) + PCBW9Offset, pcbAddr(0) + PCBFlagsOffset, pcbAddr(3) + 12, pcbAddr(2) + 8,
		pcbAddr(killTarget) + PCBFlagsOffset, pcbAddr(4), pcbAddr(4) + 8, pcbAddr(4) + 12} {
		allowed[a] = true
	}
	assertOnlyChanged(t, f, run.before, allowed, map[uint32]bool{status: true})
}

// A18: parent not waiting, so the target becomes a zombie.
func TestKillCleanupNonwaitingParent(t *testing.T) {
	t.Parallel()
	f := newKillFixture(t)
	setFamily(f, 0, none, killTarget, none, none, 9)
	setFamily(f, killTarget, 0, 2, none, none, 9)
	setFamily(f, 2, killTarget, none, none, none, 9)
	for _, p := range killPages {
		addUserPage(f, killTarget, p)
	}
	run := runKill(t, f, "schedule", len(killPages))

	assertTargetReleased(t, f, killPages)
	assertCleanupFirst(t, run)
	if got := f.get(pcbAddr(killTarget) + PCBFlagsOffset); got != 11 {
		t.Errorf("target flags %#x, want mapped zombie 0xb", got)
	}
	assertOrphaned(t, f, 2)
	allowed := cleanupAllowed(killPages)
	for _, a := range []uint32{pcbAddr(killTarget) + PCBFlagsOffset, pcbAddr(2), pcbAddr(2) + 8, pcbAddr(2) + 12} {
		allowed[a] = true
	}
	assertOnlyChanged(t, f, run.before, allowed, nil) // the parent is untouched
}

// A19: parentless target with a child.
func TestKillCleanupParentless(t *testing.T) {
	t.Parallel()
	f := newKillFixture(t)
	setFamily(f, killTarget, none, 2, none, none, 9)
	setFamily(f, 2, killTarget, none, none, none, 9)
	for _, p := range killPages {
		addUserPage(f, killTarget, p)
	}
	run := runKill(t, f, "schedule", len(killPages))

	assertTargetReleased(t, f, killPages)
	assertCleanupFirst(t, run)
	if got := f.get(pcbAddr(killTarget) + PCBFlagsOffset); got != 8 {
		t.Errorf("target flags %#x, want slot unmapped", got)
	}
	if f.m.registers[uptr] != run.uptr {
		t.Errorf("UPTR changed to %#x", f.m.registers[uptr])
	}
	assertOrphaned(t, f, 2)
	allowed := cleanupAllowed(killPages)
	for _, a := range []uint32{pcbAddr(killTarget) + PCBFlagsOffset, pcbAddr(2), pcbAddr(2) + 8, pcbAddr(2) + 12} {
		allowed[a] = true
	}
	assertOnlyChanged(t, f, run.before, allowed, nil)
}

// R8, R10: malformed metadata stops at the labeled panic before any
// lifecycle effect, even with a waiting parent.
func TestKillCleanupMalformedPanics(t *testing.T) {
	t.Parallel()
	cases := map[string]func(f *kernelFixture){
		"count mismatch": func(f *kernelFixture) { f.put(pagesUsedAddr(killTarget), 3) },
		"reserved frame": func(f *kernelFixture) { addUserPage(f, killTarget, userPage{0x10, 0x100}) },
	}
	for name, corrupt := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newKillFixture(t)
			waitingParentScene(f)
			for _, p := range killPages {
				addUserPage(f, killTarget, p)
			}
			corrupt(f)
			run := runKill(t, f, "kill_cleanup_panic", 3)
			panicPC := kernelBoundary + f.labels["kill_cleanup_panic"]
			stepInPlace(f, 1000)
			if f.m.registers[pc] != panicPC {
				t.Fatalf("PC left the panic: %#x", f.m.registers[pc])
			}
			if f.m.registers[w9] != 5 || f.m.registers[w0] != killTarget {
				t.Errorf("panic W9=%d W0=%d, want 5 and the PID", f.m.registers[w9], f.m.registers[w0])
			}
			if f.m.registers[sr]&16 != 0 {
				t.Error("interrupts enabled at the panic")
			}
			allowed := map[uint32]bool{}
			stackWords(allowed)
			assertOnlyChanged(t, f, run.before, allowed, nil) // no PTE, bitmap, count or lifecycle write
		})
	}
}
