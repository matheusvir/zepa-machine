package machine

// F2-06 verification: sackOS/kernel.asm on the F2-01 shared layout.
// Every assertion here follows execution of real kernel instructions.
// Assembling the kernel or searching its text is never the proof.
import (
	"bufio"
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	assembler "zepa-machine/cross-assembler"
)

const asmLayoutKernelPath = "../sackOS/kernel.asm"

// asmKernel is the assembled production kernel plus a label map built with the
// assembler's line rules: text after ';' is dropped, a line ending in ':' is a
// label, commas are removed, and every other non-empty line is one instruction.
type asmKernel struct {
	image    []byte
	labels   map[string]uint32 // label -> byte offset from physical 0
	highJump uint32            // byte offset of setup's JMPR to the high mapping
}

func loadASMKernel(t *testing.T) asmKernel {
	t.Helper()
	return scanASMKernel(t, asmLayoutKernelPath)
}

// scanASMKernel builds the label map and image with the assembler's line rules:
// text after ';' is dropped, a line ending in ':' is a label, commas are
// removed, and every other non-empty line is one instruction. It cross-checks
// the line count and every label operand against the assembler output, and
// locates setup's first JMPR.
func scanASMKernel(t *testing.T, path string) asmKernel {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := assembler.LoadAssemblyFromReader(strings.NewReader(string(src)))
	if err != nil {
		t.Fatal(err)
	}
	image, err := assembler.ConvertInstructionsToBinary(parsed)
	if err != nil {
		t.Fatal(err)
	}
	type reference struct {
		ins, arg int
		name     string
	}
	var refs []reference
	k := asmKernel{image: image, labels: map[string]uint32{}, highJump: ^uint32(0)}
	s := bufio.NewScanner(strings.NewReader(string(src)))
	n := 0
	for s.Scan() {
		line := strings.TrimSpace(strings.SplitN(s.Text(), ";", 2)[0])
		if strings.HasSuffix(line, ":") {
			name := strings.TrimSuffix(line, ":")
			if _, ok := k.labels[name]; ok {
				t.Fatalf("duplicate label %s", name)
			}
			k.labels[name] = uint32(n * 4)
			continue
		}
		fields := strings.Fields(strings.ReplaceAll(line, ",", ""))
		if len(fields) == 0 {
			continue
		}
		if k.highJump == ^uint32(0) && strings.EqualFold(fields[0], "JMPR") {
			k.highJump = uint32(n * 4)
		}
		for i, part := range fields[1:] {
			refs = append(refs, reference{n, i + 1, part})
		}
		n++
	}
	if err := s.Err(); err != nil {
		t.Fatal(err)
	}
	if n != len(parsed) || len(image) != n*4 {
		t.Fatalf("label map counted %d instructions, assembler produced %d (%d bytes)", n, len(parsed), len(image))
	}
	if k.highJump == ^uint32(0) {
		t.Fatal("setup high jump (JMPR) not found")
	}
	for _, r := range refs {
		if target, ok := k.labels[r.name]; ok {
			want := strconv.Itoa(int(target/4) - r.ins)
			if parsed[r.ins][r.arg] != want {
				t.Fatalf("label %s displacement: got %s want %s", r.name, parsed[r.ins][r.arg], want)
			}
		}
	}
	return k
}

// label returns a label's byte offset from physical 0, failing if it is missing.
func (k asmKernel) label(t *testing.T, name string) uint32 {
	t.Helper()
	off, ok := k.labels[name]
	if !ok {
		t.Fatalf("missing label %s", name)
	}
	return off
}

// asmAccessEvent describes the data access of one executed load or store.
type asmAccessEvent struct {
	step   int
	store  bool
	pc     uint32 // address the instruction was fetched from
	opcode Opcode
	virt   uint32 // effective address before translation
	phys   uint32 // effective physical address
	size   uint32
	kernel bool // executed in kernel mode
	viaSP  bool // STORE whose address register (rs2) is SP
}

// asmStepper runs fetch/decode/execute one instruction at a time on any machine
// and checks, before execution, that every fetch and data access translates to
// a physical address inside the backing.
type asmStepper struct {
	t          *testing.T
	m          *Machine
	labels     map[string]uint32
	phase      string
	steps      int
	diagnose   func(reason string) string // overrides diagnostic, e.g. for the fixture
	beforeStep func(pc uint32) string     // optional pre-fetch check
	onStore    []func(asmAccessEvent)
	onLoad     []func(asmAccessEvent)
	onStep     []func(inst Instruction, pcBefore uint32)
}

func newASMStepper(t *testing.T, m *Machine, labels map[string]uint32) *asmStepper {
	return &asmStepper{t: t, m: m, labels: labels}
}

// physical mirrors lookupPhysical without side effects, but never indexes
// outside the backing, so a corrupt table produces a diagnostic, not a panic.
func (s *asmStepper) physical(addr, size uint32) (uint32, string) {
	m := s.m
	backing := uint64(len(m.memory))
	if !m.isMmuEnabled() {
		if uint64(addr)+uint64(size) > backing {
			return 0, fmt.Sprintf("physical access %#x+%d beyond backing %#x", addr, size, backing)
		}
		return addr, ""
	}
	if size == 4 && addr%4 != 0 {
		return 0, fmt.Sprintf("unaligned access %#x", addr)
	}
	if !m.isKernelMode() && addr >= kernelBoundary {
		return 0, fmt.Sprintf("user access to kernel address %#x", addr)
	}
	table, page := m.registers[uptr], addr/pageSize
	if addr >= kernelBoundary {
		table, page = m.registers[kptr], (addr-kernelBoundary)/pageSize
	}
	pteAddr := uint64(table) + uint64(page)*4
	if pteAddr+4 > backing {
		return 0, fmt.Sprintf("PTE %#x for %#x beyond backing %#x", pteAddr, addr, backing)
	}
	got, ok := m.lookupPhysical(addr, size)
	if !ok {
		return 0, fmt.Sprintf("unmapped access %#x", addr)
	}
	if uint64(got)+uint64(size) > backing {
		return 0, fmt.Sprintf("access %#x -> physical %#x beyond backing %#x", addr, got, backing)
	}
	return got, ""
}

func (s *asmStepper) step() (Instruction, uint32) {
	s.t.Helper()
	m := s.m
	pcBefore := m.registers[pc]
	if s.beforeStep != nil {
		if problem := s.beforeStep(pcBefore); problem != "" {
			s.fail(problem)
		}
	}
	if _, problem := s.physical(pcBefore, 4); problem != "" {
		s.fail("fetch: " + problem)
	}
	if !m.fetch() {
		s.fail("fetch fault")
	}
	inst, ok := m.decode()
	if !ok {
		s.fail("decode fault")
	}
	if m.checkIllegalRegisterAccess(inst) || m.checkIllegalInstruction(inst) {
		s.fail("illegal instruction or register access")
	}
	var ev *asmAccessEvent
	access := func(addr, size uint32, store bool) {
		phys, problem := s.physical(addr, size)
		if problem != "" {
			s.fail(problem)
		}
		ev = &asmAccessEvent{step: s.steps, store: store, pc: pcBefore, opcode: inst.opcode, virt: addr, phys: phys, size: size,
			kernel: m.isKernelMode(), viaSP: store && inst.opcode == STORE && inst.rs2 == sp}
	}
	switch inst.opcode {
	case LOAD, STORE:
		access(m.registers[inst.rs2], 4, inst.opcode == STORE)
	case LDB, LDSB, STRB:
		access(m.registers[inst.rs2], 1, inst.opcode == STRB)
	case LDD, STRD:
		access(uint32(inst.immediate), 4, inst.opcode == STRD)
	case UDIV, SDIV:
		if m.registers[inst.rs2] == 0 {
			s.fail("division by zero")
		}
	}
	m.execute(inst)
	if ev != nil {
		hooks := s.onLoad
		if ev.store {
			hooks = s.onStore
		}
		for _, hook := range hooks {
			hook(*ev)
		}
	}
	for _, hook := range s.onStep {
		hook(inst, pcBefore)
	}
	s.steps++
	return inst, pcBefore
}

// run steps until done reports true for an executed instruction.
func (s *asmStepper) run(phase string, budget int, done func(inst Instruction, pcBefore uint32) bool) {
	s.runBudget(phase, budget, "step budget", done)
}

// runBudget is the single predicate runner. done receives each executed
// instruction and the PC it was fetched from; it decides whether to stop. The
// budget diagnostic is caller-selectable so label-reached and MRET-executed
// runs keep their distinct messages.
func (s *asmStepper) runBudget(phase string, budget int, budgetPrefix string, done func(inst Instruction, pcBefore uint32) bool) {
	s.t.Helper()
	s.phase = phase
	defer func() {
		if r := recover(); r != nil {
			s.t.Fatal(s.diagnostic(fmt.Sprintf("panic: %v", r)))
		}
	}()
	for i := 0; i < budget; i++ {
		if done(s.step()) {
			return
		}
	}
	s.fail(fmt.Sprintf("%s %d exhausted", budgetPrefix, budget))
}

func (s *asmStepper) fail(reason string) {
	s.t.Helper()
	s.t.Fatal(s.diagnostic(reason))
}

func (s *asmStepper) diagnostic(reason string) string {
	if s.diagnose != nil {
		return s.diagnose(reason)
	}
	r := s.m.registers
	return fmt.Sprintf("%s: phase=%s step=%d PC=%#x label=%s SR=%#x SP=%#x KPTR=%#x UPTR=%#x ECR=%d EFA=%#x W0=%#x W1=%#x W9=%#x",
		reason, s.phase, s.steps, r[pc], nearestLabel(s.labels, r[pc]), r[sr], r[sp], r[kptr], r[uptr], r[ecr], r[efa], r[w0], r[w1], r[w9])
}

// nearestLabel names the closest label at or before addr. Addresses at or above
// the kernel boundary are treated as kernel-virtual offsets.
func nearestLabel(labels map[string]uint32, addr uint32) string {
	if addr >= kernelBoundary {
		addr -= kernelBoundary
	}
	best, name := uint32(0), ""
	for label, off := range labels {
		if off <= addr && (name == "" || off > best || (off == best && label < name)) {
			best, name = off, label
		}
	}
	if name == "" {
		return "<none>"
	}
	return fmt.Sprintf("%s+%#x", name, addr-best)
}

func physWord(m *Machine, addr uint32) uint32 {
	return binary.LittleEndian.Uint32(m.memory[addr : addr+4])
}

func TestKernelASMLayoutStepperSelfTest(t *testing.T) {
	k := loadASMKernel(t)
	m := NewMachine(32<<20, false)
	m.LoadProgram(k.image)
	m.registers[w0] = uint32(len(m.memory))
	s := newASMStepper(t, m, k.labels)
	var stores []asmAccessEvent
	s.onStore = append(s.onStore, func(ev asmAccessEvent) { stores = append(stores, ev) })
	// setup: STRD memory_size; MV UPTR; MV W1; ADD UPTR; MV W1; MV W9; SHL W9; STORE W9, UPTR
	for i := 0; i < 8; i++ {
		s.step()
	}
	memorySize := uint32(GlobalAddress(GlobalMemorySizeOffset))
	pcb0PTE := uint32(PCBVectorStart) + PCBPageTableOffset
	if len(stores) != 2 {
		t.Fatalf("stores = %+v, want STRD and STORE", stores)
	}
	if ev := stores[0]; ev.opcode != STRD || ev.phys != memorySize || ev.size != 4 || !ev.kernel || ev.viaSP || ev.pc != 0 {
		t.Fatalf("first store %+v, want STRD at %#x from PC 0", ev, memorySize)
	}
	if ev := stores[1]; ev.opcode != STORE || ev.phys != pcb0PTE || ev.virt != pcb0PTE || ev.viaSP || ev.step != 7 {
		t.Fatalf("second store %+v, want STORE at %#x on step 7", ev, pcb0PTE)
	}
	if physWord(m, memorySize) != uint32(len(m.memory)) || physWord(m, pcb0PTE) != isPteMappedMask {
		t.Fatalf("stored memory_size=%#x pte=%#x", physWord(m, memorySize), physWord(m, pcb0PTE))
	}
	// A kernel-mode STORE addressed by SP is reported as such.
	m.registers[sp] = 0x100
	m.registers[pc] = 0x200
	binary.BigEndian.PutUint32(m.memory[0x200:], uint32(STORE)<<26|uint32(w3)<<16|uint32(sp)<<11)
	s.step()
	if ev := stores[2]; ev.opcode != STORE || !ev.viaSP || ev.phys != 0x100 {
		t.Fatalf("SP store %+v", ev)
	}
}

func TestKernelASMLayoutSize(t *testing.T) {
	k := loadASMKernel(t)
	if uint32(len(k.image)) > uint32(KernelCodeEnd) {
		t.Fatalf("kernel is %#x bytes, code reservation ends at %#x", len(k.image), KernelCodeEnd)
	}
	if k.highJump+4 > pageSize {
		t.Fatalf("setup high jump at %#x is outside identity-mapped page 0", k.highJump)
	}
	t.Logf("kernel %d bytes (%d instructions), high jump at %#x", len(k.image), len(k.image)/4, k.highJump)
}

const (
	asmKPTR         = uint32(KernelPageTableStart)
	asmBitmap       = uint32(BitmapStart)
	asmKernelFrames = uint32(PhysicalMemoryEnd) / pageSize // frames [0, 0x40000) are kernel-reserved
	asmLegacyKPTR   = uint32(0x30007024)                   // legacy KPTR start, below the shared table
	asmSetupBudget  = 2_000_000
	asmTimeSlice    = 2 // host time slice, in clock interrupts
	// Kernel stack reservation, kernel-virtual: [0xFFFC0000, 0xFFFD0000).
	asmKernelStackTop    = uint32(KernelStackEnd) + KernelVirtualOffset
	asmKernelStackBottom = uint32(KernelStackStart) + KernelVirtualOffset
	asmStatusAddr        = uint32(ControlRegionStart) + ControlStatusOffset // physical 0x3FFD0100
	asmHandlerBudget     = 500_000
	// kill runs page_release before any lifecycle change: one read-only pass
	// over the whole 0xC0000-entry user table (about 3.1 million instructions)
	// and a release pass that stops after PID 0's single page.
	asmKillBudget = 5_000_000
)

// asmSetupRun is a machine with the runner's minimum backing on which the real
// kernel setup has executed from PC 0 through its final MRET.
type asmSetupRun struct {
	k        asmKernel
	m        *Machine
	s        *asmStepper
	legacy   []asmAccessEvent // kernel data accesses below 0x4000 or stores to the legacy KPTR
	finalPC  uint32           // kernel-virtual address of setup's final MRET
	idlePC   uint32           // kernel-virtual address of the idle JUMP #0
	esaWant  uint32
	setupLen int

	// Readiness: the setup step whose execution first made status.ready 1, the
	// structure check taken at that moment, and every store executed after it.
	inSetup          bool
	readyStep        int
	readyProblem     string
	storesAfterReady []asmAccessEvent
}

// A kernel data access below 0x4000 lands in the code reservation, which holds
// no data (this includes the legacy globals [0x2000, 0x2024)); a store between
// the legacy and the shared KPTR start would write the legacy table.
func isLegacyLayoutAccess(ev asmAccessEvent) bool {
	return (ev.kernel && ev.phys < uint32(KernelCodeEnd)) ||
		(ev.store && ev.phys >= asmLegacyKPTR && ev.phys < asmKPTR)
}

func newASMSetupRun(t *testing.T) *asmSetupRun {
	t.Helper()
	k := loadASMKernel(t)
	m := NewMachine(int(RunnerMinimumBackingBytes), false)
	m.LoadProgram(k.image)
	// The host globals, written as main_kernel.go writes them before Boot.
	binary.LittleEndian.PutUint32(m.memory[GlobalAddress(GlobalMaxProcessesOffset):], PCBCount)
	binary.LittleEndian.PutUint32(m.memory[GlobalAddress(GlobalTimeSliceOffset):], asmTimeSlice)
	binary.LittleEndian.PutUint32(m.memory[GlobalAddress(GlobalBufferSizeOffset):], TransferBufferBytes)
	m.registers[w0] = uint32(len(m.memory)) // as Boot sets it
	supervisor := k.label(t, "exception_supervisor")
	r := &asmSetupRun{k: k, m: m, s: newASMStepper(t, m, k.labels),
		finalPC: kernelBoundary + supervisor - 8, idlePC: kernelBoundary + supervisor - 4, esaWant: kernelBoundary + supervisor}
	legacy := func(ev asmAccessEvent) {
		if isLegacyLayoutAccess(ev) {
			r.legacy = append(r.legacy, ev)
		}
	}
	r.s.onStore = append(r.s.onStore, legacy)
	r.s.onLoad = append(r.s.onLoad, legacy)
	r.readyStep = -1
	readyAddr := asmStatusAddr + StatusReadyOffset
	r.s.onStore = append(r.s.onStore, func(ev asmAccessEvent) {
		if r.inSetup && r.readyStep >= 0 && ev.step > r.readyStep {
			r.storesAfterReady = append(r.storesAfterReady, ev)
		}
	})
	r.s.onStep = append(r.s.onStep, func(Instruction, uint32) {
		if r.inSetup && r.readyStep < 0 && physWord(m, readyAddr) == 1 {
			r.readyStep = r.s.steps
			r.readyProblem = r.checkSetupStructures()
		}
	})
	return r
}

// runSetup executes setup from PC 0 to its final MRET.
func (r *asmSetupRun) runSetup() {
	if got := physWord(r.m, asmStatusAddr+StatusReadyOffset); got != 0 {
		r.s.t.Fatalf("status.ready = %#x before setup, want 0", got)
	}
	r.inSetup = true
	defer func() { r.inSetup = false }()
	r.s.run("setup", asmSetupBudget, func(inst Instruction, pcBefore uint32) bool {
		return inst.opcode == MRET && pcBefore == r.finalPC
	})
	r.setupLen = r.s.steps
}

// checkSetupStructures reports the first difference between memory and the
// final values setup is required to write, or "" when all of them hold.
func (r *asmSetupRun) checkSetupStructures() string {
	m := r.m
	if got := m.registers[kptr]; got != asmKPTR {
		return fmt.Sprintf("KPTR register %#x, want %#x", got, asmKPTR)
	}
	for i := uint32(0); i < asmKernelFrames; i++ {
		if got := physWord(m, asmKPTR+i*4); got != isPteMappedMask|i {
			return fmt.Sprintf("kernel PTE %#x at %#x = %#x, want %#x", i, asmKPTR+i*4, got, isPteMappedMask|i)
		}
	}
	for w := uint32(0); w < asmKernelFrames/32; w++ {
		if got := physWord(m, asmBitmap+w*4); got != ^uint32(0) {
			return fmt.Sprintf("bitmap word %#x for frames %#x.. = %#x, want all ones", asmBitmap+w*4, w*32, got)
		}
	}
	if got := physWord(m, asmBitmap+asmKernelFrames/8); got&1 != 0 {
		return fmt.Sprintf("bitmap bit for frame %#x is set (word %#x)", asmKernelFrames, got)
	}
	if got := physWord(m, uint32(GlobalAddress(GlobalMemorySizeOffset))); got != uint32(len(m.memory)) {
		return fmt.Sprintf("memory_size %#x, want %#x", got, len(m.memory))
	}
	if got := physWord(m, uint32(GlobalAddress(GlobalRunningPIDOffset))); got != ^uint32(0) {
		return fmt.Sprintf("running_pid %#x, want -1", got)
	}
	if got := physWord(m, uint32(GlobalAddress(GlobalKernelStackPointerOffset))); got != asmKernelStackTop {
		return fmt.Sprintf("kernel_stack_pointer %#x, want %#x", got, asmKernelStackTop)
	}
	if got := physWord(m, uint32(PCBVectorStart)+PCBPageTableOffset); got != 0 {
		return fmt.Sprintf("PCB 0 identity entry at %#x = %#x, want invalidated", uint32(PCBVectorStart)+PCBPageTableOffset, got)
	}
	return ""
}

func TestKernelASMLayoutSetup(t *testing.T) {
	r := newASMSetupRun(t)
	r.runSetup()
	m := r.m
	if problem := r.checkSetupStructures(); problem != "" {
		t.Fatal(problem)
	}
	if m.registers[pc] != r.idlePC || m.registers[esa] != r.esaWant || m.registers[sr] != 48 {
		t.Fatalf("after setup PC=%#x ESA=%#x SR=%#x, want idle %#x, exception_supervisor %#x, SR 48",
			m.registers[pc], m.registers[esa], m.registers[sr], r.idlePC, r.esaWant)
	}
	if len(r.legacy) != 0 {
		t.Fatalf("setup accessed legacy layout addresses: %+v", r.legacy)
	}
	// Readiness is the final setup store, published with every structure final.
	if r.readyStep < 0 {
		t.Fatal("setup never set status.ready")
	}
	if r.readyProblem != "" {
		t.Fatalf("status.ready became 1 on step %d before setup was complete: %s", r.readyStep, r.readyProblem)
	}
	if len(r.storesAfterReady) != 0 {
		t.Fatalf("setup stored after status.ready (step %d): %+v", r.readyStep, r.storesAfterReady)
	}
	control := uint32(ControlRegionStart)
	for off, want := range map[uint32]uint32{StatusVersionOffset: ControlProtocolVersion, StatusPhaseOffset: uint32(PhaseReady), StatusReadyOffset: 1} {
		if got := physWord(m, asmStatusAddr+off); got != want {
			t.Fatalf("status word +%#x = %#x, want %#x", off, got, want)
		}
	}
	for _, rng := range []struct {
		name       string
		start, end uint32
	}{
		{"status reserved", asmStatusAddr + StatusReservedOffset, asmStatusAddr + ControlStatusSize},
		{"bootstrap", control + ControlBootstrapOffset, control + ControlBootstrapOffset + ControlBootstrapSize},
		{"executable bootstrap bytes", control + ControlExecutableBytesOffset, control + ControlExecutableBytesOffset + ControlExecutableBytesSize},
		{"image descriptor", control + ControlImageDescriptorOffset, control + ControlImageDescriptorOffset + ControlImageDescriptorSize},
		{"request", control + ControlRequestOffset, control + ControlRequestOffset + ControlRequestSize},
		{"result", control + ControlResultOffset, control + ControlResultOffset + ControlResultSize},
		{"event", control + ControlEventOffset, control + ControlEventOffset + ControlEventSize},
		{"image window", uint32(ImageWindowStart), uint32(ImageWindowEnd)},
	} {
		for a := rng.start; a < rng.end; a += 4 {
			if got := physWord(m, a); got != 0 {
				t.Fatalf("%s word %#x = %#x after setup, want 0", rng.name, a, got)
			}
		}
	}
	t.Logf("setup ran %d instructions to the final MRET at %#x; status.ready set on step %d", r.setupLen, r.finalPC, r.readyStep)
}

// One-page user program from asm/samples, loaded through the transfer buffer.
func asmLayoutSample(t *testing.T) []byte {
	t.Helper()
	image, err := assembler.RunAssembler("../asm/samples/infinite_loop.asm")
	if err != nil {
		t.Fatal(err)
	}
	if len(image) == 0 || len(image) > pageSize {
		t.Fatalf("sample is %d bytes, want one page", len(image))
	}
	return image
}

func TestKernelASMLayoutProcessLifecycle(t *testing.T) {
	r := newASMSetupRun(t)
	r.runSetup()
	m, s := r.m, r.s
	sample := asmLayoutSample(t)
	pcb0 := uint32(PCBVectorStart)
	firstFrame := asmKernelFrames // the only frame the minimum backing has above the reservation
	bitWord := asmBitmap + firstFrame/8
	stack := trackKernelStack(s, kernelBoundary+r.k.label(t, "schedule"))

	// Input interrupt from the idle loop, raised as Boot raises it.
	if len(sample) > bufferSize-4 {
		t.Fatal("sample does not fit the transfer buffer")
	}
	// The test raises the interrupt itself, so it writes the buffer directly
	// instead of going through the safe-point request path.
	m.writeTransferBufferLocked(sample)
	m.exception(inputInt)
	stack.begin()
	s.run("input_int", asmHandlerBudget, func(Instruction, uint32) bool { return !m.isKernelMode() })
	stack.check(t, "input_int")
	inputDepth := stack.maxDepth
	if !stack.used || inputDepth != 16 {
		t.Fatalf("input_int kernel stack used=%v depth=%d bytes, want 16", stack.used, inputDepth)
	}
	if got := physWord(m, uint32(GlobalAddress(GlobalRunningPIDOffset))); got != 0 {
		t.Fatalf("running_pid %d after input, want 0", got)
	}
	pte := physWord(m, pcb0+PCBPageTableOffset)
	if pte&isPteMappedMask == 0 || pte&pteFrameMask < asmKernelFrames {
		t.Fatalf("process PTE 0 = %#x, want a valid frame >= %#x", pte, asmKernelFrames)
	}
	frame := pte & pteFrameMask
	if frame != firstFrame || physWord(m, bitWord)&1 == 0 {
		t.Fatalf("frame %#x, bitmap word %#x = %#x; want frame %#x with bit 0 set", frame, bitWord, physWord(m, bitWord), firstFrame)
	}
	if got := m.memory[frame*pageSize : frame*pageSize+uint32(len(sample))]; string(got) != string(sample) {
		t.Fatal("process frame does not hold the sample")
	}
	for page := uint32(1); page < 768*1024; page++ {
		if physWord(m, pcb0+PCBPageTableOffset+page*4)&isPteMappedMask != 0 {
			t.Fatalf("unexpected PTE %#x mapped", page)
		}
	}

	// Clock interrupts: the time slice the host stored at 0x4004 decides when
	// clock_int hands over to the scheduler.
	clockCount := uint32(GlobalAddress(GlobalClockCountOffset))
	schedule := kernelBoundary + r.k.label(t, "schedule")
	scheduled := false
	s.onStep = append(s.onStep, func(_ Instruction, pcBefore uint32) {
		if pcBefore == schedule {
			scheduled = true
		}
	})
	for tick := uint32(1); tick <= asmTimeSlice; tick++ {
		m.exception(clockInt)
		s.run("clock_int", asmHandlerBudget, func(Instruction, uint32) bool { return !m.isKernelMode() })
		wantCount, wantScheduled := tick, false
		if tick == asmTimeSlice {
			wantCount, wantScheduled = 0, true
		}
		if got := physWord(m, clockCount); got != wantCount || scheduled != wantScheduled {
			t.Fatalf("clock tick %d: clock_interrupt_count %d, scheduled %v; want %d, %v", tick, got, scheduled, wantCount, wantScheduled)
		}
	}
	if got := physWord(m, uint32(GlobalAddress(GlobalRunningPIDOffset))); got != 0 || m.registers[pc] != 0 {
		t.Fatalf("after the time slice: running_pid %d PC %#x, want PID 0 resumed at 0", got, m.registers[pc])
	}

	// Kill interrupt for PID 0 while it is running.
	pid := make([]byte, 4)
	m.writeTransferBufferLocked(pid)
	m.exception(killInt)
	loop := kernelBoundary + r.k.label(t, "loop")
	stack.begin()
	s.run("kill_int", asmKillBudget, func(Instruction, uint32) bool { return m.registers[pc] == loop })
	stack.check(t, "kill_int")
	if got := physWord(m, bitWord); got&1 != 0 {
		t.Fatalf("bitmap word %#x = %#x after kill, want bit 0 clear", bitWord, got)
	}
	if got := physWord(m, pcb0+PCBPageTableOffset); got&isPteMappedMask != 0 {
		t.Fatalf("process PTE 0 = %#x after kill, want invalid", got)
	}
	if got := physWord(m, pcb0+PCBFlagsOffset) & 1; got != 0 {
		t.Fatalf("PCB 0 still mapped after kill")
	}
	if len(r.legacy) != 0 {
		t.Fatalf("legacy layout addresses accessed: %+v", r.legacy)
	}
	// Releasing the frame restored every setup structure; in particular no
	// kernel PTE changed, including those at the legacy bitmap base 0x30107024.
	if problem := r.checkSetupStructures(); problem != "" {
		t.Fatalf("after kill: %s", problem)
	}
	// Neither setup nor the handlers wrote the image window or any control
	// record other than the kernel status record.
	zero := func(name string, start, end uint32) {
		for a := start; a < end; a += 4 {
			if got := physWord(m, a); got != 0 {
				t.Fatalf("%s word %#x = %#x, want 0", name, a, got)
			}
		}
	}
	control := uint32(ControlRegionStart)
	zero("image window", uint32(ImageWindowStart), uint32(ImageWindowEnd))
	zero("control bootstrap", control, control+ControlStatusOffset)
	zero("control records", control+ControlStatusOffset+ControlStatusSize, control+ControlUsedBytes)
	t.Logf("setup %d, input+clock+kill %d instructions; input_int kernel stack depth %d bytes", r.setupLen, s.steps-r.setupLen, inputDepth)
}

// asmStackTracker checks, during one handler run up to the scheduler, that every
// kernel-mode STORE addressed by SP lands in the kernel stack reservation, that
// SP never leaves it once the handler has pushed, and that SP is back at the
// stored top when the handler reaches schedule.
type asmStackTracker struct {
	schedule uint32
	active   bool
	used     bool
	reached  bool
	maxDepth uint32
	problems []string
}

func trackKernelStack(s *asmStepper, schedule uint32) *asmStackTracker {
	k := &asmStackTracker{schedule: schedule}
	s.onStore = append(s.onStore, func(ev asmAccessEvent) {
		if !k.active || !ev.kernel || !ev.viaSP {
			return
		}
		k.used = true
		if ev.virt < asmKernelStackBottom || ev.virt >= asmKernelStackTop {
			k.problems = append(k.problems, fmt.Sprintf("step %d PC %#x: SP store at %#x outside [%#x, %#x)", ev.step, ev.pc, ev.virt, asmKernelStackBottom, asmKernelStackTop))
		} else if depth := asmKernelStackTop - ev.virt; depth > k.maxDepth {
			k.maxDepth = depth
		}
	})
	s.onStep = append(s.onStep, func(_ Instruction, pcBefore uint32) {
		if !k.active {
			return
		}
		top := s.m.registers[sp]
		if s.m.registers[pc] == k.schedule {
			k.active, k.reached = false, true
			if k.used && top != asmKernelStackTop {
				k.problems = append(k.problems, fmt.Sprintf("SP %#x at schedule, want %#x", top, asmKernelStackTop))
			}
			return
		}
		if k.used && (top < asmKernelStackBottom || top > asmKernelStackTop) {
			k.problems = append(k.problems, fmt.Sprintf("step %d PC %#x: SP %#x left the reservation", s.steps, pcBefore, top))
		}
	})
	return k
}

func (k *asmStackTracker) begin() {
	k.active, k.used, k.reached, k.maxDepth, k.problems = true, false, false, 0, nil
}

func (k *asmStackTracker) check(t *testing.T, handler string) {
	t.Helper()
	if !k.reached {
		t.Fatalf("%s: schedule was not reached", handler)
	}
	if len(k.problems) > 0 {
		t.Fatalf("%s kernel stack: %s", handler, strings.Join(k.problems, "; "))
	}
}

// fork on the compact fixture, with the production stack backed by it.
func TestKernelASMLayoutForkStack(t *testing.T) {
	f := newKernelFixture(t)
	f.identityPCBPages(0, 1)
	parent := uint32(PCBVectorStart)
	child := parent + PCBStride
	f.put(uint32(GlobalAddress(GlobalMaxProcessesOffset)), 2)
	f.put(uint32(GlobalAddress(GlobalRunningPIDOffset)), 0)
	if got := f.get(uint32(GlobalAddress(GlobalKernelStackPointerOffset))); got != asmKernelStackTop {
		t.Fatalf("fixture kernel_stack_pointer %#x, want %#x", got, asmKernelStackTop)
	}
	f.put(parent+PCBChildOffset, ^uint32(0))
	f.put(parent+PCBFlagsOffset, 1)
	parentFrame := f.next
	f.next += pageSize
	f.put(parent+PCBPageTableOffset, isPteMappedMask|parentFrame/pageSize)
	f.put(parent+PCBPagesUsedOffset, 1)
	f.initBitmap()
	s := newASMStepper(t, f.m, f.labels)
	stack := trackKernelStack(s, kernelBoundary+f.labels["schedule"])
	f.m.registers[pc] = kernelBoundary + f.labels["fork"]
	stack.begin()
	s.run("fork", 20_000_000, func(Instruction, uint32) bool { return stack.reached })
	stack.check(t, "fork")
	if f.get(parent+PCBW9Offset) != 1 || f.get(child+PCBW9Offset) != ^uint32(1) || f.get(child+PCBPagesUsedOffset) != 1 {
		t.Fatalf("fork result parent=%#x child=%#x child pages=%d", f.get(parent+PCBW9Offset), f.get(child+PCBW9Offset), f.get(child+PCBPagesUsedOffset))
	}
	if !stack.used || stack.maxDepth != 24 {
		t.Fatalf("fork kernel stack used=%v depth=%d bytes, want 24", stack.used, stack.maxDepth)
	}
	t.Logf("fork ran %d instructions; kernel stack depth %d bytes", s.steps, stack.maxDepth)
}
