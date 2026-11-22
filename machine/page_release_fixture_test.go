package machine

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"
	"testing"
)

// F2-02 test support. kill now calls page_release before any lifecycle
// effect. The helper needs a mapped kernel stack (kill loads SP from
// kernel_stack_pointer) and reads every PTE of the target's full table, even
// when pages_used is 0, so those pages must be backed in the compact fixture.

// killCleanupStack is the top page of the production kernel stack
// reservation. The fixture already backs it and points kernel_stack_pointer
// at its top (0xFFFD0000), the value setup leaves there.
const killCleanupStack = uint32(KernelStackEnd) - pageSize

// killCleanupBudget covers page_release for a target without valid PTEs (one
// read-only pass over the full table, about 3.1 million instructions; the
// release pass stops at once) plus kill and the dispatch around it.
const killCleanupBudget = 5_000_000

const (
	pcbPagesUsedOffset = 76
	pcbTableOffset     = 80
	userTableEntries   = 0xC0000
	pageReleaseFrame   = 48         // bytes of kernel stack used by page_release
	testMemorySize     = 0x80000000 // 2 GiB: 0x80000 frames
	firstUserFrame     = 0x40000    // frames below are reserved by setup
	pcbVectorEnd       = uint32(PCBVectorEnd)
)

// prepareKillCleanup maps a kernel stack and the whole PCB, including the
// 0x300000-byte user table, of each target PID.
func prepareKillCleanup(f *kernelFixture, pids ...uint32) {
	f.t.Helper()
	f.mapKernel(killCleanupStack)
	f.put(uint32(GlobalAddress(GlobalKernelStackPointerOffset)), kernelBoundary+killCleanupStack+pageSize)
	for _, pid := range pids {
		start := pcbAddr(pid)
		for addr := start &^ (pageSize - 1); addr < start+PCBStride; addr += pageSize {
			f.mapKernel(addr)
		}
	}
}

func pcbAddr(pid uint32) uint32          { return uint32(PCBVectorStart) + pid*PCBStride }
func pteAddr(pid, index uint32) uint32   { return pcbAddr(pid) + pcbTableOffset + index*4 }
func pagesUsedAddr(pid uint32) uint32    { return pcbAddr(pid) + pcbPagesUsedOffset }
func bitmapWordAddr(frame uint32) uint32 { return uint32(BitmapStart) + frame/32*4 }
func stackTop() uint32                   { return kernelBoundary + killCleanupStack + pageSize }

// mapPCBHeader backs the first 80 bytes of a PCB (family links, saved
// context, flags and pages_used).
func mapPCBHeader(f *kernelFixture, pid uint32) {
	f.t.Helper()
	start := pcbAddr(pid)
	for addr := start &^ (pageSize - 1); addr < start+pcbTableOffset; addr += pageSize {
		f.mapKernel(addr)
	}
}

// Logical user frames (>= 0x40000) are never dereferenced by page_release;
// only their bitmap bits are backed.
func setFrameBit(f *kernelFixture, frame uint32, occupied bool) {
	f.t.Helper()
	addr := bitmapWordAddr(frame)
	f.mapKernel(addr)
	word := f.get(addr)
	if occupied {
		word |= 1 << (frame % 32)
	} else {
		word &^= 1 << (frame % 32)
	}
	f.put(addr, word)
}

func frameBit(f *kernelFixture, frame uint32) uint32 {
	return f.get(bitmapWordAddr(frame)) >> (frame % 32) & 1
}

type userPage struct{ index, frame uint32 }

// addUserPage installs a consistent mapping: valid PTE, occupied bitmap bit
// and pages_used + 1.
func addUserPage(f *kernelFixture, pid uint32, p userPage) {
	f.t.Helper()
	f.put(pteAddr(pid, p.index), isPteMappedMask|p.frame)
	setFrameBit(f, p.frame, true)
	f.put(pagesUsedAddr(pid), f.get(pagesUsedAddr(pid))+1)
}

// memAccess is one load or store observed before the instruction executes.
// addr is a kernel address (virtual minus the boundary) unless user
// is set, in which case it is the raw user virtual address.
type memAccess struct {
	step  int
	store bool
	user  bool
	addr  uint32
	value uint32
	uptr  uint32
}

// runTraced executes like kernelFixture.run (same guards and diagnostics)
// and reports every memory access to visit. It returns the steps executed.
func runTraced(f *kernelFixture, from, until string, limit int, visit func(memAccess)) int {
	f.t.Helper()
	start, ok := f.labels[from]
	if !ok {
		f.t.Fatalf("missing label %s", from)
	}
	end, ok := f.labels[until]
	if !ok {
		f.t.Fatalf("missing label %s", until)
	}
	f.phase = from
	f.m.registers[pc] = kernelBoundary + start
	for i := 0; i < limit; i++ {
		current := f.m.registers[pc]
		if current == kernelBoundary+end {
			return i
		}
		if current < kernelBoundary {
			f.fail("PC escaped kernel")
		}
		if _, ok := f.pages[(current-kernelBoundary)/pageSize]; !ok {
			f.fail("PC is unmapped")
		}
		if !f.m.fetch() {
			f.fail("fetch fault")
		}
		inst, ok := f.m.decode()
		if !ok {
			f.fail("decode fault")
		}
		if f.m.checkIllegalRegisterAccess(inst) || f.m.checkIllegalInstruction(inst) {
			f.fail("illegal instruction or register access")
		}
		if visit != nil {
			var a memAccess
			traced := true
			switch inst.opcode {
			case LOAD, LDB, LDSB:
				a.addr = f.m.registers[inst.rs2]
			case STORE, STRB:
				a.store, a.addr, a.value = true, f.m.registers[inst.rs2], f.m.registers[inst.rs1]
			case LDD:
				a.addr = uint32(inst.immediate)
			case STRD:
				a.store, a.addr, a.value = true, uint32(inst.immediate), f.m.registers[inst.rd]
			default:
				traced = false
			}
			if traced {
				a.step, a.uptr = i, f.m.registers[uptr]
				if a.addr >= kernelBoundary {
					a.addr -= kernelBoundary
				} else {
					a.user = true
				}
				visit(a)
			}
		}
		f.m.execute(inst)
		if f.m.registers[pc] < kernelBoundary {
			f.fail("exception or PC escaped kernel")
		}
	}
	f.fail(fmt.Sprintf("step budget %d exhausted", limit))
	return limit
}

// stepInPlace executes n more instructions from the current PC.
func stepInPlace(f *kernelFixture, n int) {
	f.t.Helper()
	for i := 0; i < n; i++ {
		if !f.m.fetch() {
			f.fail("fetch fault")
		}
		inst, ok := f.m.decode()
		if !ok {
			f.fail("decode fault")
		}
		f.m.execute(inst)
	}
}

func snapshotMemory(f *kernelFixture) []byte {
	return append([]byte(nil), f.m.memory...)
}

// assertOnlyChanged compares memory with before and fails on any changed
// word that is neither an allowed kernel address nor an allowed
// physical address (for memory outside the kernel mapping, such as a user
// frame).
func assertOnlyChanged(t *testing.T, f *kernelFixture, before []byte, allowed, allowedPhysical map[uint32]bool) {
	t.Helper()
	pageOf := map[uint32]uint32{}
	for page, frame := range f.pages {
		pageOf[frame] = page
	}
	var unexpected []string
	mem := f.m.memory
	for base := 0; base < len(mem); base += pageSize {
		if bytes.Equal(mem[base:base+pageSize], before[base:base+pageSize]) {
			continue
		}
		for off := 0; off < pageSize; off += 4 {
			p := base + off
			if binary.LittleEndian.Uint32(mem[p:]) == binary.LittleEndian.Uint32(before[p:]) {
				continue
			}
			if page, ok := pageOf[uint32(base)]; ok {
				kernelAddr := page*pageSize + uint32(off)
				if !allowed[kernelAddr] {
					unexpected = append(unexpected, fmt.Sprintf("kernel %#x", kernelAddr))
				}
			} else if !allowedPhysical[uint32(p)] {
				unexpected = append(unexpected, fmt.Sprintf("physical %#x", p))
			}
		}
	}
	if len(unexpected) > 0 {
		sort.Strings(unexpected)
		if len(unexpected) > 20 {
			unexpected = append(unexpected[:20], "...")
		}
		t.Fatalf("unexpected memory writes: %v", unexpected)
	}
}

// stackWords allows the page_release frame below the stack top.
func stackWords(allowed map[uint32]bool) {
	top := killCleanupStack + pageSize
	for a := top - pageReleaseFrame; a < top; a += 4 {
		allowed[a] = true
	}
}

// callerSentinels are distinct live values the helper must preserve.
var preservedRegisters = []Register{w0, w1, w2, w3, w4, w8, k0, k1, uptr, kptr, sp, ecr, esa, esr, epc, efa}

func setCallerSentinels(f *kernelFixture) map[Register]uint32 {
	r := f.m.registers
	r[w0], r[w1], r[w2], r[w3], r[w4] = 0x10101010, 0x21212121, 0x32323232, 0x43434343, 0x54545454
	r[w5], r[w6], r[w7], r[w9] = 0x65656565, 0x76767676, 0x87878787, 0x98989898
	r[k0], r[k1] = 0xC0FFEE00, 0x0BADF00D
	r[ecr], r[esa], r[esr], r[epc], r[efa] = 3, 0x00123000, 0x28, 0x40, 0xDEADBEE0
	r[w8] = kernelBoundary + f.labels["loop"] // continuation, never reached otherwise
	r[sp] = stackTop()
	r[sr] = 32 | 4 // kernel mode, MMU on, interrupts off, a comparison flag set
	saved := map[Register]uint32{}
	for _, reg := range preservedRegisters {
		saved[reg] = r[reg]
	}
	return saved
}

func assertPreserved(t *testing.T, f *kernelFixture, saved map[Register]uint32) {
	t.Helper()
	for _, reg := range preservedRegisters {
		if got := f.m.registers[reg]; got != saved[reg] {
			t.Errorf("register %s = %#x, want preserved %#x", getRegisterName(reg), got, saved[reg])
		}
	}
	if got := f.m.registers[sr] &^ 7; got != 32 {
		t.Errorf("SR non-comparison bits = %#x, want 0x20", got)
	}
}
