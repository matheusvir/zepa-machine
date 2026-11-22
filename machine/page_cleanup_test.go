package machine

import (
	"testing"

	assembler "zepa-machine/cross-assembler"
)

// Direct calls of page_release (F2-02) through the real kernel instructions.
// The target is PID 1 while PID 0 is the running process and UPTR belongs to
// neither, so every case also checks independence from running_pid and the
// active user table (A10).

const cleanupPID = 1

type cleanupRun struct {
	result  uint32
	before  []byte
	saved   map[Register]uint32
	stores  []memAccess
	outside []memAccess // PCB-vector accesses other than the target count and table
}

func newCleanupFixture(t *testing.T) *kernelFixture {
	t.Helper()
	f := newKernelFixture(t)
	f.put(uint32(GlobalAddress(GlobalMemorySizeOffset)), testMemorySize)
	f.put(uint32(GlobalAddress(GlobalMaxProcessesOffset)), 4)
	f.put(uint32(GlobalAddress(GlobalRunningPIDOffset)), 0) // another process is running
	prepareKillCleanup(f, cleanupPID)
	pcb := pcbAddr(cleanupPID)
	for off := uint32(0); off < pcbPagesUsedOffset; off += 4 {
		f.put(pcb+off, 0xA5000000|off) // links, status address, saved context, flags and padding
	}
	return f
}

func cleanupBudget(validPages int) int { return 10_000_000 + 5_000_000*validPages }

func callPageRelease(t *testing.T, f *kernelFixture, pcbVA, tableVA uint32, budget int) cleanupRun {
	t.Helper()
	run := cleanupRun{before: snapshotMemory(f)}
	run.saved = setCallerSentinels(f)
	f.m.registers[w7], f.m.registers[w6] = pcbVA, tableVA
	pcb := pcbAddr(cleanupPID)
	runTraced(f, "page_release", "loop", budget, func(a memAccess) {
		if a.store {
			run.stores = append(run.stores, a)
		}
		inStack := a.addr >= killCleanupStack && a.addr < killCleanupStack+pageSize
		if !a.user && !inStack && a.addr >= uint32(PCBVectorStart) && a.addr < pcbVectorEnd &&
			a.addr != pcb+pcbPagesUsedOffset && (a.addr < pcb+pcbTableOffset || a.addr >= pcb+PCBStride) {
			run.outside = append(run.outside, a)
		}
	})
	run.result = f.m.registers[w9]
	return run
}

func releaseTarget(t *testing.T, f *kernelFixture, validPages int) cleanupRun {
	t.Helper()
	pcb := kernelBoundary + pcbAddr(cleanupPID)
	return callPageRelease(t, f, pcb, pcb+pcbTableOffset, cleanupBudget(validPages))
}

func assertReleased(t *testing.T, f *kernelFixture, run cleanupRun, pages []userPage) {
	t.Helper()
	if run.result != 0 {
		t.Fatalf("page_release W9 = %d, want 0", run.result)
	}
	allowed := map[uint32]bool{pagesUsedAddr(cleanupPID): true}
	stackWords(allowed)
	pteStores := map[uint32]int{}
	for _, p := range pages {
		allowed[pteAddr(cleanupPID, p.index)] = true
		allowed[bitmapWordAddr(p.frame)] = true
		if got := f.get(pteAddr(cleanupPID, p.index)); got != 0 {
			t.Errorf("PTE %#x = %#x, want invalid", p.index, got)
		}
		if frameBit(f, p.frame) != 0 {
			t.Errorf("frame %#x still occupied", p.frame)
		}
	}
	if got := f.get(pagesUsedAddr(cleanupPID)); got != 0 {
		t.Errorf("persisted pages_used = %d, want 0", got)
	}
	assertOnlyChanged(t, f, run.before, allowed, nil)
	assertPreserved(t, f, run.saved)
	if len(run.outside) > 0 {
		t.Fatalf("accesses outside the target count/table: %+v", run.outside[0])
	}

	// One store per released PTE, one persisted decrement per release, and no
	// release write after the count reached zero (R4, R5, A07).
	table := pcbAddr(cleanupPID) + pcbTableOffset
	var counts []uint32
	lastZero := -1
	for _, s := range run.stores {
		switch {
		case s.addr >= table && s.addr < table+userTableEntries*4:
			pteStores[s.addr]++
			if s.value != 0 {
				t.Errorf("PTE store %#x = %#x, want 0", s.addr, s.value)
			}
			if lastZero >= 0 {
				t.Errorf("PTE store at step %d after pages_used reached zero", s.step)
			}
		case s.addr == pagesUsedAddr(cleanupPID):
			counts = append(counts, s.value)
			if s.value == 0 {
				lastZero = s.step
			}
		case s.addr >= uint32(BitmapStart) && s.addr < uint32(BitmapEnd) && lastZero >= 0:
			t.Errorf("bitmap store at step %d after pages_used reached zero", s.step)
		}
	}
	for _, p := range pages {
		if n := pteStores[pteAddr(cleanupPID, p.index)]; n != 1 {
			t.Errorf("PTE %#x stored %d times, want once", p.index, n)
		}
	}
	if len(pteStores) != len(pages) {
		t.Errorf("stores to %d PTEs, want %d", len(pteStores), len(pages))
	}
	if len(counts) != len(pages) {
		t.Fatalf("pages_used stored %d times, want %d: %v", len(counts), len(pages), counts)
	}
	for i, c := range counts {
		if want := uint32(len(pages) - 1 - i); c != want {
			t.Fatalf("pages_used store sequence %v, want one decrement per release", counts)
		}
	}
}

func assertRejected(t *testing.T, f *kernelFixture, run cleanupRun) {
	t.Helper()
	if run.result != 5 {
		t.Fatalf("page_release W9 = %d, want 5", run.result)
	}
	allowed := map[uint32]bool{}
	stackWords(allowed)
	assertOnlyChanged(t, f, run.before, allowed, nil)
	assertPreserved(t, f, run.saved)
	if len(run.outside) > 0 {
		t.Fatalf("accesses outside the target count/table: %+v", run.outside[0])
	}
}

func installPages(f *kernelFixture, pages []userPage) {
	for _, p := range pages {
		addUserPage(f, cleanupPID, p)
	}
}

// A01, A10, R2: empty table, called twice, from a nonrunning target.
func TestPageCleanupEmptyAndRepeated(t *testing.T) {
	t.Parallel()
	f := newCleanupFixture(t)
	for call := 1; call <= 2; call++ {
		run := releaseTarget(t, f, 0)
		assertReleased(t, f, run, nil)
		if f.get(uint32(GlobalAddress(GlobalRunningPIDOffset))) != 0 {
			t.Fatalf("call %d changed running_pid", call)
		}
	}
}

// A02-A06, A08: valid entries at the ends, spread over both halves, around
// the center (the table has no single middle entry) and a partial image.
func TestPageCleanupLayouts(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		indices []uint32
	}{
		{"first page", []uint32{0}},
		{"last page", []uint32{0xBFFFF}},
		{"code data and stack", []uint32{0, 1, 2, 0xBFFFE, 0xBFFFF}},
		{"sparse both halves", []uint32{3, 0x1234, 0x5FF00, 0x60100, 0x9ABCD, 0xBFFF0}},
		{"adjacent middle entries", []uint32{0x5FFFF, 0x60000}},
		{"single central entry", []uint32{0x5FFFD}},
		{"partial image", []uint32{0, 1, 0x40}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newCleanupFixture(t)
			var pages []userPage
			for i, index := range tc.indices {
				pages = append(pages, userPage{index, firstUserFrame + 0x100 + uint32(i)})
			}
			installPages(f, pages)
			run := releaseTarget(t, f, len(pages))
			assertReleased(t, f, run, pages)
		})
	}
}

// A07: the count reaches zero on the low side, on the high side, and after a
// release on each side.
func TestPageCleanupCountReachesZeroOnEitherSide(t *testing.T) {
	t.Parallel()
	for name, indices := range map[string][]uint32{
		"low side":  {5},
		"high side": {0xBFFF0},
		"both":      {5, 0xBFFF0},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newCleanupFixture(t)
			var pages []userPage
			for i, index := range indices {
				pages = append(pages, userPage{index, firstUserFrame + 0x200 + uint32(i)})
			}
			installPages(f, pages)
			assertReleased(t, f, releaseTarget(t, f, len(pages)), pages)
		})
	}
}

// A09: flags and padding hold sentinels distinct from the count at +76.
func TestPageCleanupFlagsPaddingAndCount(t *testing.T) {
	t.Parallel()
	f := newCleanupFixture(t)
	flags := pcbAddr(cleanupPID) + 72
	f.put(flags, 0x5A5A5AA5)
	pages := []userPage{{0, 0x40300}, {0xBFFFF, 0x40301}}
	installPages(f, pages)
	assertReleased(t, f, releaseTarget(t, f, len(pages)), pages)
	if got := f.get(flags); got != 0x5A5A5AA5 {
		t.Fatalf("flags/padding = %#x, want unchanged", got)
	}
}

// A11, R6: bit positions 0 and 31 of a shared word, then a repeated call.
func TestPageCleanupBitmapWordBoundaries(t *testing.T) {
	t.Parallel()
	f := newCleanupFixture(t)
	const word = firstUserFrame + 0x40 // frames 0x40040..0x4005F share one bitmap word
	for frame := uint32(word); frame < word+32; frame++ {
		setFrameBit(f, frame, true) // unrelated occupied neighbours
	}
	pages := []userPage{{0x10, word}, {0xBFF00, word + 31}}
	for _, p := range pages {
		f.put(pteAddr(cleanupPID, p.index), isPteMappedMask|p.frame)
	}
	f.put(pagesUsedAddr(cleanupPID), 2)
	assertReleased(t, f, releaseTarget(t, f, 2), pages)
	if got := f.get(bitmapWordAddr(word)); got != 0x7FFFFFFE {
		t.Fatalf("bitmap word = %#x, want only bits 0 and 31 cleared", got)
	}
	assertReleased(t, f, releaseTarget(t, f, 0), nil) // repeat: no toggling
	if got := f.get(bitmapWordAddr(word)); got != 0x7FFFFFFE {
		t.Fatalf("repeated cleanup changed bitmap word to %#x", got)
	}
}

// A12: another process's mappings and frames and the reserved kernel frames
// stay untouched during ordinary cleanup.
func TestPageCleanupProtectedResources(t *testing.T) {
	t.Parallel()
	f := newCleanupFixture(t)
	prepareKillCleanup(f, 2)
	other := []userPage{{0, 0x40500}, {0xBFFFF, 0x40501}}
	for _, p := range other {
		addUserPage(f, 2, p)
	}
	kernelWord := f.get(uint32(BitmapStart))
	pages := []userPage{{0, 0x40400}, {0xBFFFF, 0x40401}}
	installPages(f, pages)
	assertReleased(t, f, releaseTarget(t, f, len(pages)), pages) // the memory diff covers every other byte
	for _, p := range other {
		if f.get(pteAddr(2, p.index)) != isPteMappedMask|p.frame || frameBit(f, p.frame) != 1 {
			t.Fatalf("PID 2 page %#x was released", p.index)
		}
	}
	if f.get(pagesUsedAddr(2)) != 2 || f.get(uint32(BitmapStart)) != kernelWord {
		t.Fatal("PID 2 count or reserved kernel bits changed")
	}
}

// R7: memory size zero encodes 4 GiB, so the last frame is valid.
func TestPageCleanupFourGiBMemorySize(t *testing.T) {
	t.Parallel()
	f := newCleanupFixture(t)
	f.put(uint32(GlobalAddress(GlobalMemorySizeOffset)), 0)
	pages := []userPage{{7, 0xFFFFF}}
	installPages(f, pages)
	assertReleased(t, f, releaseTarget(t, f, 1), pages)
}

// A13: malformed frame references are rejected before any write.
func TestPageCleanupMalformedFrames(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		setup func(f *kernelFixture)
	}{
		{"reserved frame", func(f *kernelFixture) { addUserPage(f, cleanupPID, userPage{4, 0x100}) }},
		{"last reserved frame", func(f *kernelFixture) { addUserPage(f, cleanupPID, userPage{4, firstUserFrame - 1}) }},
		{"kernel-mapped frame", func(f *kernelFixture) { addUserPage(f, cleanupPID, userPage{4, 0x2}) }},
		{"frame at memory limit", func(f *kernelFixture) { addUserPage(f, cleanupPID, userPage{4, 0x80000}) }},
		{"frame already free", func(f *kernelFixture) {
			addUserPage(f, cleanupPID, userPage{4, 0x40600})
			setFrameBit(f, 0x40600, false)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newCleanupFixture(t)
			addUserPage(f, cleanupPID, userPage{0x100, 0x40700}) // a valid page that must survive
			tc.setup(f)
			assertRejected(t, f, releaseTarget(t, f, 3))
		})
	}
}

// Duplicate frames inside the target table are not an invariant error:
// map_page never creates them, and releasing the same frame twice is harmless
// because the bitmap bit is cleared with AND and the count still has to match.
// The helper releases both PTEs, frees the frame and reaches a zero count.
func TestPageCleanupDuplicateFrameIsReleasedOnce(t *testing.T) {
	t.Parallel()
	for name, other := range map[string]uint32{
		"before block boundary": 2,
		"in a later block":      5,
		"at the far end":        0xBFFFF,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newCleanupFixture(t)
			pages := []userPage{{0, 0x40800}, {other, 0x40800}}
			addUserPage(f, cleanupPID, pages[0])
			f.put(pteAddr(cleanupPID, other), isPteMappedMask|0x40800)
			f.put(pagesUsedAddr(cleanupPID), 2)
			assertReleased(t, f, releaseTarget(t, f, 2), pages)
		})
	}
}

// A14, A15: count/table disagreement in both directions.
func TestPageCleanupCountMismatch(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		valid int
		count uint32
	}{
		{"count exceeds valid entries", 2, 3},
		{"count below valid entries", 2, 1},
		{"zero count with a valid entry", 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newCleanupFixture(t)
			for i := 0; i < tc.valid; i++ {
				addUserPage(f, cleanupPID, userPage{uint32(i) * 0x30000, 0x40900 + uint32(i)})
			}
			f.put(pagesUsedAddr(cleanupPID), tc.count)
			assertRejected(t, f, releaseTarget(t, f, tc.valid))
		})
	}
}

// R2: W7 must be an exact PCB base below max_processes and W6 = W7 + 80.
func TestPageCleanupInvalidPointers(t *testing.T) {
	t.Parallel()
	pcb := kernelBoundary + pcbAddr(cleanupPID)
	cases := []struct {
		name           string
		pcbVA, tableVA uint32
	}{
		{"below the PCB vector", kernelBoundary + uint32(PCBVectorStart) - 4, kernelBoundary + uint32(PCBVectorStart) + 76},
		{"inside a PCB", pcb + 4, pcb + 84},
		{"slot beyond max_processes", kernelBoundary + pcbAddr(4), kernelBoundary + pcbAddr(4) + pcbTableOffset},
		{"table not at PCB + 80", pcb, pcb + 84},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newCleanupFixture(t)
			addUserPage(f, cleanupPID, userPage{0, 0x40A00})
			assertRejected(t, f, callPageRelease(t, f, tc.pcbVA, tc.tableVA, 1000))
		})
	}
}

// A16: a connected process is cleaned without any lifecycle effect.
func TestPageCleanupConnectedProcess(t *testing.T) {
	t.Parallel()
	f := newCleanupFixture(t)
	for _, pid := range []uint32{2, 3} {
		mapPCBHeader(f, pid)
	}
	pcb := pcbAddr(cleanupPID)
	f.put(pcb, 0)             // parent
	f.put(pcb+4, 3)           // child
	f.put(pcb+8, 2)           // previous sibling
	f.put(pcb+12, ^uint32(0)) // next sibling
	f.put(pcb+16, 0x1008)     // status address
	f.put(pcb+72, 9)          // mapped, ready
	f.put(pcbAddr(0)+72, 5)   // parent mapped and waiting
	f.put(pcbAddr(0)+4, 2)
	f.put(pcbAddr(2)+12, cleanupPID)
	f.put(pcbAddr(3), cleanupPID)
	pages := []userPage{{0, 0x40B00}, {1, 0x40B01}, {0xBFFFF, 0x40B02}}
	installPages(f, pages)
	assertReleased(t, f, releaseTarget(t, f, len(pages)), pages) // no other kernel or user word changed
}

// R11, R12: the real kernel still fits below the globals at 0x4000, keeps
// the PCB geometry and has a single cleanup algorithm.
func TestPageCleanupKernelImage(t *testing.T) {
	t.Parallel()
	image, err := assembler.RunAssembler("../sackOS/kernel.asm")
	if err != nil {
		t.Fatal(err)
	}
	if len(image) > int(KernelCodeEnd) {
		t.Fatalf("kernel is %#x bytes, limit %#x", len(image), uint32(KernelCodeEnd))
	}
	f := newKernelFixture(t) // also checks every label displacement
	for _, name := range []string{"page_release", "page_release_check", "page_release_one", "kill_cleanup_panic"} {
		if _, ok := f.labels[name]; !ok {
			t.Errorf("missing label %s", name)
		}
	}
	for _, name := range []string{"kill_free_memory_loop", "kill_skip_i", "kill_skip_j", "kill_end"} {
		if _, ok := f.labels[name]; ok {
			t.Errorf("old cleanup label %s still present", name)
		}
	}
	if (PCBStride-pcbTableOffset)/4 != userTableEntries {
		t.Fatalf("PCB geometry changed: stride %#x", PCBStride)
	}
}
