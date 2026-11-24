package machine

// F2-07 stage 1: explicit relocation table for the F2-01 kernel layout.
//
// Every entry names one production instruction by the label it belongs to and
// its instruction ordinal from that label, plus the literal that instruction
// must produce. The table covers the layout values that move when the physical
// or virtual layout moves: the nine globals, the PCB base and stride, the KPTR
// and bitmap constructions, the kernel stack top and the control status record.
// The values that must not move with the layout (the 3 GiB boundary, the page
// table size and the kernel page threshold) are checked by a separate table.
//
// Entries are verified by executing the real cloned kernel instructions through
// the shared asmStepper predicate runner. The image and labels come from the
// shared scanASMKernel path used by the compact fixture, so this file neither
// reimplements a handler nor reads a literal out of the source text.
//
// Only the bitmap bases built after setup are executable patches: map_page
// (sackOS/kernel.asm:2297-2306) and page_release (sackOS/kernel.asm:2535-2540).
// The F2-02 page_release helper replaced kill's inline release loop, so the
// construction formerly at orphanize_end now lives in page_release; orphanize_end
// only jumps to schedule (sackOS/kernel.asm:1901). Each patch supplies the
// relocated high half and a zero low half: a zero low half cannot be
// sign-extended by MV, so the patched address is independent of the #0x9024
// sign extension. KPTR, the stack top and the status record are verify-only.
import (
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
)

// fixtureRelocatedBitmap is the compact, 64 KiB-aligned bitmap base used by the
// executable-patch check. Its zero low half is what makes the patched high/low
// pair immune to MV sign extension.
const fixtureRelocatedBitmap = 0x1800000

// relocationAnchor names one instruction: a label plus the number of
// instructions between that label and the anchored instruction.
type relocationAnchor struct {
	label   string
	ordinal int
}

func (a relocationAnchor) String() string {
	return fmt.Sprintf("%s+%d", a.label, a.ordinal)
}

type probeKind uint8

const (
	probeImmediate probeKind = iota
	probeRegister
)

// bitmapPatch marks the two bitmap-base instructions that may be rewritten for
// compact RAM: the high-half MV and the low-half MV, as ordinals from the
// anchor's label, plus the ordinal of the ADD that combines the halves. result
// and boundary name the registers the patched site uses for the assembled base
// and the kernel boundary: map_page uses W0/K0, page_release uses W4/W5.
type bitmapPatch struct {
	high     int
	low      int
	virtual  int
	result   Register
	boundary Register
}

// layoutProbe is one table entry. kind selects whether the expected literal is
// the anchored instruction's immediate or the named register after execution.
type layoutProbe struct {
	name     string
	anchor   relocationAnchor
	start    int // first ordinal to execute; register probes default to 0
	want     uint32
	kind     probeKind
	register Register
	prepare  func(*Machine)
	patch    *bitmapPatch
}

func registerProbe(register Register, value uint32) func(*Machine) {
	return func(m *Machine) {
		m.registers[register] = value
	}
}

func (p layoutProbe) observe(m *Machine, inst Instruction) uint32 {
	if p.kind == probeImmediate {
		return uint32(inst.immediate)
	}
	return m.registers[p.register]
}

// kernelRelocations is the explicit layout relocation list. Globals are checked
// at an instruction that carries the literal, so a missing or changed literal
// fails even when a later computation happens to land on the right value.
var kernelRelocations = []layoutProbe{
	// Nine global words, 0x4000 through 0x4020 in four-byte steps.
	{name: "global.max_processes", anchor: relocationAnchor{"schedule", 7}, want: 0x4000, kind: probeImmediate},
	{name: "global.time_slice", anchor: relocationAnchor{"clock_int", 8}, want: 0x4004, kind: probeImmediate},
	{name: "global.buffer_size", anchor: relocationAnchor{"input_int", 4}, want: 0x4008, kind: probeImmediate},
	{name: "global.memory_size", anchor: relocationAnchor{"map_page", 9}, want: 0x400C, kind: probeImmediate},
	{name: "global.running_pid", anchor: relocationAnchor{"schedule", 15}, want: 0x4010, kind: probeImmediate},
	{name: "global.clock_count", anchor: relocationAnchor{"schedule", 3}, want: 0x4014, kind: probeImmediate},
	{name: "global.kernel_stack_pointer", anchor: relocationAnchor{"fork_copy_regs_done", 16}, want: 0x4018, kind: probeImmediate},
	{name: "global.scratch0", anchor: relocationAnchor{"fork_copy_regs_done", 13}, want: 0x401C, kind: probeImmediate},
	{name: "global.scratch1", anchor: relocationAnchor{"fork_copy_regs_done", 5}, want: 0x4020, kind: probeImmediate},

	// PCB vector base and stride.
	{name: "pcb.base", anchor: relocationAnchor{"getPID", 3}, want: 0x4024, kind: probeImmediate},
	{name: "pcb.stride", anchor: relocationAnchor{"schedule", 14}, start: 10, want: 0x300050, kind: probeRegister, register: w6},

	// KPTR construction: #0x3001 << 16 plus #0x9024.
	{name: "kptr.high", anchor: relocationAnchor{"setup", 8}, want: 0x3001, kind: probeImmediate},
	{name: "kptr.low", anchor: relocationAnchor{"setup", 11}, want: 0x9024, kind: probeImmediate},
	{name: "kptr.base", anchor: relocationAnchor{"setup", 14}, start: 8, want: 0x30009024, kind: probeRegister, register: kptr},

	// Bitmap construction: #0x3011 << 16 plus #0x9024.
	{name: "bitmap.high", anchor: relocationAnchor{"setup", 41}, want: 0x3011, kind: probeImmediate},
	{name: "bitmap.low", anchor: relocationAnchor{"setup", 44}, want: 0x9024, kind: probeImmediate},
	{name: "bitmap.base", anchor: relocationAnchor{"setup", 45}, start: 41, want: 0x30109024, kind: probeRegister, register: w0},

	// Bitmap extension span: #15 shifts the one-byte step into 2^15 bytes,
	// the full 1 GiB bitmap (sackOS/kernel.asm:77-79). Verify-only; the compact
	// fixture cannot populate that span.
	{name: "bitmap.extension", anchor: relocationAnchor{"setup", 48}, want: 15, kind: probeImmediate},

	// Kernel stack top and control status record.
	{name: "stack.top", anchor: relocationAnchor{"setup", 64}, start: 62, want: 0xFFFD0000, kind: probeRegister, register: sp},
	{name: "status.physical", anchor: relocationAnchor{"setup", 72}, start: 68, want: 0x3FFD0100, kind: probeRegister, register: w0},

	// Executable patches: the bitmap bases after setup. kill no longer builds
	// one: it passes the target PCB to page_release, which repeats the
	// construction before storing the address on its stack (sackOS/kernel.asm:2535).
	// The setup construction is verified above, and the KPTR, stack and status
	// constructions never change.
	{name: "bitmap.base.map_page", anchor: relocationAnchor{"map_page", 4}, start: 0, want: 0x30109024, kind: probeRegister, register: w0, patch: &bitmapPatch{high: 0, low: 3, virtual: 8, result: w0, boundary: k0}},
	{name: "bitmap.base.page_release", anchor: relocationAnchor{"page_release", 59}, start: 55, want: 0x30109024, kind: probeRegister, register: w4, patch: &bitmapPatch{high: 55, low: 58, virtual: 60, result: w4, boundary: w5}},
}

// kernelNonRelocations are values that must survive a layout move unchanged.
var kernelNonRelocations = []layoutProbe{
	{name: "kernel.boundary", anchor: relocationAnchor{"schedule", 2}, want: 0xC0000000, kind: probeRegister, register: w8},
	{name: "page_table.entries", anchor: relocationAnchor{"fork_copy_regs_done", 28}, start: 26, want: 786432, kind: probeRegister, register: w3},
	{name: "kernel_page.threshold", anchor: relocationAnchor{"map_page", 77}, start: 76, want: 0xC0000, kind: probeRegister, register: w1, prepare: registerProbe(k0, kernelBoundary)},
}

func (f *kernelFixture) snapshotRegisters() map[Register]uint32 {
	snapshot := make(map[Register]uint32, len(f.m.registers))
	for register, value := range f.m.registers {
		snapshot[register] = value
	}
	return snapshot
}

func (f *kernelFixture) restoreRegisters(snapshot map[Register]uint32) {
	clear(f.m.registers)
	for register, value := range snapshot {
		f.m.registers[register] = value
	}
}

// runLayoutProbe executes the probe's instruction span, from its start ordinal
// through the anchor ordinal, and returns the value the entry observes.
// Immediate probes execute only the anchored instruction, so a literal is
// checked without preceding control flow or memory accesses. Every probe starts
// from the fixture's register snapshot.
func (f *kernelFixture) runLayoutProbe(snapshot map[Register]uint32, p layoutProbe) uint32 {
	f.t.Helper()
	f.restoreRegisters(snapshot)
	if p.prepare != nil {
		p.prepare(f.m)
	}
	start, ok := f.labels[p.anchor.label]
	if !ok {
		f.t.Fatalf("relocation %s: missing label %s", p.name, p.anchor.label)
	}
	first := p.start
	if p.kind == probeImmediate {
		first = p.anchor.ordinal
	}
	if first < 0 || first > p.anchor.ordinal {
		f.t.Fatalf("relocation %s anchor=%s: start ordinal %d outside probe", p.name, p.anchor, first)
	}
	f.phase = p.name
	f.m.registers[pc] = kernelBoundary + start + uint32(first)*4
	s := f.runner("relocation probe escaped kernel")
	steps := p.anchor.ordinal - first + 1
	var last Instruction
	executed := 0
	s.runBudget(p.name, steps, "relocation step budget", func(inst Instruction, _ uint32) bool {
		last = inst
		executed++
		return executed == steps
	})
	return p.observe(f.m, last)
}

// layoutMismatch returns the same text verifyLayoutProbe reports for got, so
// the in-process self-test can assert the diagnostic path without failing.
func (f *kernelFixture) layoutMismatch(p layoutProbe, got uint32) string {
	return fmt.Sprintf("relocation %s anchor=%s: expected %#x got %#x: %s",
		p.name, p.anchor, p.want, got, f.diagnosticText("relocation literal mismatch"))
}

func (f *kernelFixture) verifyLayoutProbe(snapshot map[Register]uint32, p layoutProbe) {
	f.t.Helper()
	got := f.runLayoutProbe(snapshot, p)
	if got != p.want {
		f.t.Fatal(f.layoutMismatch(p, got))
	}
}

// relocationProbe finds a table entry by name, so the self-test cannot drift
// from the relocation table it exercises.
func relocationProbe(t *testing.T, name string) layoutProbe {
	t.Helper()
	for _, p := range kernelRelocations {
		if p.name == name {
			return p
		}
	}
	t.Fatalf("missing relocation %s", name)
	return layoutProbe{}
}

// adulterateImmediate rewrites one instruction's immediate field, leaving its
// opcode and register fields intact, so a probe observes a different literal.
func (f *kernelFixture) adulterateImmediate(p layoutProbe, value uint16) {
	f.t.Helper()
	base, ok := f.labels[p.anchor.label]
	if !ok {
		f.t.Fatalf("relocation %s: missing label %s", p.name, p.anchor.label)
	}
	offset := base + uint32(p.anchor.ordinal)*4
	physical := f.phys(offset)
	word := binary.BigEndian.Uint32(f.m.memory[physical:])
	word = word&^(uint32(immediateBitMask)<<5) | uint32(value)<<5
	binary.BigEndian.PutUint32(f.m.memory[physical:], word)
}

// applyBitmapPatch rewrites the high and low immediates of a bitmap-base
// construction. The low half is explicitly zero, so MV cannot sign-extend it.
func (f *kernelFixture) applyBitmapPatch(p layoutProbe, relocated uint32) {
	f.t.Helper()
	if p.patch == nil {
		f.t.Fatalf("relocation %s anchor=%s is verify-only", p.name, p.anchor)
	}
	if relocated&0xFFFF != 0 {
		f.t.Fatalf("relocated bitmap %#x is not 64 KiB aligned, low half would not be zero", relocated)
	}
	base, ok := f.labels[p.anchor.label]
	if !ok {
		f.t.Fatalf("relocation %s: missing label %s", p.name, p.anchor.label)
	}
	for _, immediate := range []struct {
		ordinal int
		value   uint16
	}{
		{p.patch.high, uint16(relocated >> 16)},
		{p.patch.low, 0},
	} {
		offset := base + uint32(immediate.ordinal)*4
		physical := f.phys(offset)
		word := binary.BigEndian.Uint32(f.m.memory[physical:])
		word = word&^(uint32(immediateBitMask)<<5) | uint32(immediate.value)<<5
		binary.BigEndian.PutUint32(f.m.memory[physical:], word)
	}
}

func TestKernelControlRelocationTable(t *testing.T) {
	for _, p := range kernelRelocations {
		f := newKernelFixture(t)
		f.verifyLayoutProbe(f.snapshotRegisters(), p)
	}
}

// TestKernelControlRelocationMismatchDiagnostic adulterates one table literal
// and asserts the same diagnostic verifyLayoutProbe would emit, without
// replacing the real instruction execution.
func TestKernelControlRelocationMismatchDiagnostic(t *testing.T) {
	cases := []struct {
		name  string
		probe string
		value uint16
	}{
		{name: "global literal", probe: "global.max_processes", value: 0x4001},
		{name: "bitmap extension literal", probe: "bitmap.extension", value: 14},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := relocationProbe(t, c.probe)
			if p.kind != probeImmediate {
				t.Fatalf("mismatch self-test %s needs an immediate probe", p.name)
			}
			f := newKernelFixture(t)
			f.adulterateImmediate(p, c.value)
			got := f.runLayoutProbe(f.snapshotRegisters(), p)
			if got != uint32(c.value) {
				t.Fatalf("%s: observed %#x, want adulterated %#x", p.name, got, c.value)
			}
			if got == p.want {
				t.Fatalf("%s: adulterated literal still matched %#x", p.name, p.want)
			}
			text := f.layoutMismatch(p, got)
			for _, want := range []string{
				"relocation " + p.name,
				"anchor=" + p.anchor.String(),
				fmt.Sprintf("expected %#x", p.want),
				fmt.Sprintf("got %#x", got),
				"relocation literal mismatch",
				"phase=" + p.name,
			} {
				if !strings.Contains(text, want) {
					t.Errorf("%s diagnostic missing %q: %s", p.name, want, text)
				}
			}
		})
	}
}

func TestKernelControlNonRelocationTable(t *testing.T) {
	if kernelBoundary != 0xC0000000 {
		t.Fatalf("kernel boundary = %#x, want #0xC0000000", kernelBoundary)
	}
	for _, p := range kernelNonRelocations {
		f := newKernelFixture(t)
		f.verifyLayoutProbe(f.snapshotRegisters(), p)
	}
}

func TestKernelControlBitmapRelocationPatch(t *testing.T) {
	patched := 0
	for _, p := range kernelRelocations {
		if p.patch == nil {
			continue
		}
		if p.name != "bitmap.base.map_page" && p.name != "bitmap.base.page_release" {
			t.Fatalf("relocation %s must be verify-only", p.name)
		}
		patched++
		f := newKernelFixture(t)
		snapshot := f.snapshotRegisters()
		f.applyBitmapPatch(p, fixtureRelocatedBitmap)
		if got := f.runLayoutProbe(snapshot, p); got != fixtureRelocatedBitmap {
			f.t.Fatalf("patched bitmap %s anchor=%s: expected %#x got %#x: %s",
				p.name, p.anchor, fixtureRelocatedBitmap, got, f.diagnosticText("bitmap patch mismatch"))
		}
		virtual := layoutProbe{
			name:     p.name + ".virtual",
			anchor:   relocationAnchor{p.anchor.label, p.patch.virtual},
			start:    p.start,
			want:     kernelBoundary + fixtureRelocatedBitmap,
			kind:     probeRegister,
			register: p.patch.result,
			prepare:  registerProbe(p.patch.boundary, kernelBoundary),
		}
		if got := f.runLayoutProbe(snapshot, virtual); got != virtual.want {
			f.t.Fatalf("patched bitmap %s anchor=%s: expected %#x got %#x: %s",
				virtual.name, virtual.anchor, virtual.want, got, f.diagnosticText("bitmap virtual patch mismatch"))
		}
	}
	if patched != 2 {
		t.Fatalf("executable bitmap patches = %d, want map_page and page_release", patched)
	}
}

// F2-07 stage 2: deterministic frame selection for the compact fixture.
//
// map_page scans the bitmap from word 0, least significant bit first, and stops
// at memory_size/4096 frames (sackOS/kernel.asm:2398-2433). A fixture can only
// force a non-contiguous allocation or an exact partial exhaustion if tests can
// edit the bitmap after initBitmap has reserved the compact prefix. These
// helpers shape the bitmap and then run the real map_page; they do not replace
// it with a Go allocator.
//
// Remaining carry-over checklist for later F2-07 stages:
//   - Keep the full-tier 1 GiB + 4 KiB stage pending (RunnerMinimumBackingBytes,
//     machine/kernel_layout.go:20). Stage 2 shapes only the compact bitmap.
//   - PR body: state the relation to PR11 (fix/f2-09-kernel-transfer-buffer-test-25,
//     merge 5c1a7b5). Both branches replace the removed LoadBuffer in
//     kernel_asm_layout_test.go; rebase on PR11 and drop the duplicate hunk.

// frameSelectionUPTR is a dedicated user page-table page above the fixture's
// KPTR. The default fixtureUPTR (0x1100000) ends exactly at KPTR's end, so
// map_page's pages_used word (UPTR-4) would alias KPTR's last entry.
const frameSelectionUPTR = 0x1200000

// frameSelectionNone is the want-list marker for "map_page finds no frame".
const frameSelectionNone = ^uint32(0)

// frameBit locates a frame in the little-endian bitmap: the 32-frame word
// address and the least-significant-first bit used by map_page.
func (f *kernelFixture) frameBit(frame uint32) (word, bit uint32) {
	return uint32(BitmapStart) + (frame/32)*4, 1 << (frame % 32)
}

// frameReserved reports whether the bitmap marks frame unavailable to map_page.
func (f *kernelFixture) frameReserved(frame uint32) bool {
	word, bit := f.frameBit(frame)
	return f.get(word)&bit != 0
}

// frameFree is the inverse of frameReserved.
func (f *kernelFixture) frameFree(frame uint32) bool {
	return !f.frameReserved(frame)
}

// frameCount is the number of frames map_page scans for the fixture backing.
func (f *kernelFixture) frameCount() uint32 {
	return uint32(len(f.m.memory)) / pageSize
}

// firstFreeFrame returns the first frame map_page can select.
func (f *kernelFixture) firstFreeFrame() uint32 {
	f.t.Helper()
	for frame := uint32(0); frame < f.frameCount(); frame++ {
		if f.frameFree(frame) {
			return frame
		}
	}
	f.t.Fatal("fixture has no free frame")
	return 0
}

// freeFrameCount counts frames map_page would consider free.
func (f *kernelFixture) freeFrameCount() uint32 {
	var free uint32
	for frame := uint32(0); frame < f.frameCount(); frame++ {
		if f.frameFree(frame) {
			free++
		}
	}
	return free
}

// reserveFrames marks [from, from+count) reserved through the bitmap OR, the
// same bit update map_page performs before it returns a frame.
func (f *kernelFixture) reserveFrames(from, count uint32) {
	f.t.Helper()
	for frame := from; frame < from+count; frame++ {
		word, bit := f.frameBit(frame)
		f.put(word, f.get(word)|bit)
	}
}

// releaseFrames clears [from, from+count), creating a free hole below the
// fixture's next sequential allocation.
func (f *kernelFixture) releaseFrames(from, count uint32) {
	f.t.Helper()
	for frame := from; frame < from+count; frame++ {
		word, bit := f.frameBit(frame)
		f.put(word, f.get(word)&^bit)
	}
}

// identityFrames makes each physical frame reachable through KPTR at its own
// page index, following the identityPCBPages pattern in kernel_fixture_test.go.
// map_page writes its user page table and pages_used through that kernel window.
func (f *kernelFixture) identityFrames(frames ...uint32) {
	f.t.Helper()
	for _, frame := range frames {
		addr := frame * pageSize
		if old, ok := f.pages[frame]; ok && old == addr {
			continue
		}
		if old, ok := f.pages[frame]; ok {
			copy(f.m.memory[addr:addr+pageSize], f.m.memory[old:old+pageSize])
		}
		f.pages[frame] = addr
		binary.LittleEndian.PutUint32(f.m.memory[fixtureKPTR+frame*4:], isPteMappedMask|frame)
	}
}

// prepareFrameSelection gives map_page a writable user page table and a zero
// pages_used word. Both pages are identity mapped so the kernel-window stores
// land in the frame the tables describe.
func (f *kernelFixture) prepareFrameSelection() {
	f.t.Helper()
	f.identityFrames(frameSelectionUPTR/pageSize, frameSelectionUPTR/pageSize-1)
	f.put(frameSelectionUPTR-4, 0)
	f.m.registers[uptr] = frameSelectionUPTR
}

// runMapPage executes the real map_page for one user page id and stops at its
// returning JMPR. found is false when map_page reports allocation failure.
func (f *kernelFixture) runMapPage(page uint32) (frame uint32, found bool) {
	f.t.Helper()
	start, ok := f.labels["map_page"]
	if !ok {
		f.t.Fatal("missing label map_page")
	}
	const returnSentinel = ^uint32(0)
	f.phase = "map_page"
	f.m.registers[pc] = kernelBoundary + start
	f.m.registers[w9] = page
	f.m.registers[w8] = returnSentinel
	s := f.runner("map_page escaped kernel")
	s.runBudget("map_page", 1_000_000, "map_page step budget", func(inst Instruction, _ uint32) bool {
		return inst.opcode == JMPR
	})
	if got := f.m.registers[pc]; got != returnSentinel {
		f.t.Fatalf("map_page returned to %#x, want sentinel %#x", got, returnSentinel)
	}
	if f.m.registers[w9] != 0 {
		return 0, false
	}
	return f.m.registers[w7] & (isPteMappedMask - 1), true
}

// TestKernelControlFrameSelection shapes the bitmap, then executes map_page and
// checks the selected frames plus the bitmap reservation it leaves behind.
func TestKernelControlFrameSelection(t *testing.T) {
	cases := []struct {
		name  string
		exact bool // exactly len(want)-1 free bits must remain before allocation
		shape func(*kernelFixture) []uint32
	}{
		{
			name: "contiguous",
			shape: func(f *kernelFixture) []uint32 {
				tail := f.firstFreeFrame()
				return []uint32{tail, tail + 1}
			},
		},
		{
			name:  "partial exhaustion",
			exact: true,
			shape: func(f *kernelFixture) []uint32 {
				tail := f.firstFreeFrame()
				f.reserveFrames(tail+2, f.frameCount()-tail-2)
				return []uint32{tail, tail + 1, frameSelectionNone}
			},
		},
		{
			name:  "hole before first free",
			exact: true,
			shape: func(f *kernelFixture) []uint32 {
				tail := f.firstFreeFrame()
				hole := tail / 2
				f.releaseFrames(hole, 1)
				f.reserveFrames(tail+1, f.frameCount()-tail-1)
				return []uint32{hole, tail, frameSelectionNone}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newKernelFixture(t)
			f.prepareFrameSelection()
			want := c.shape(f)
			if c.exact {
				if free := f.freeFrameCount(); free != uint32(len(want)-1) {
					t.Fatalf("%s: free bits = %d, want %d", c.name, free, len(want)-1)
				}
			}
			for i, expected := range want {
				frame, found := f.runMapPage(uint32(i))
				if expected == frameSelectionNone {
					if found {
						t.Fatalf("%s: allocation %d found frame %d, want exhaustion", c.name, i, frame)
					}
					continue
				}
				if !found || frame != expected {
					t.Fatalf("%s: allocation %d = frame %d found=%v, want %d", c.name, i, frame, found, expected)
				}
				if !f.frameReserved(frame) {
					t.Fatalf("%s: allocated frame %d is still free in the bitmap", c.name, frame)
				}
			}
		})
	}
}
