package assembler

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
)

// Samples and marker layout introduced by F2-04. Values are 16-bit positives so
// that MV encodes them without sign extension.
const (
	execIntermediateSample = "../asm/samples/exec_intermediate.asm"
	execDestinationSample  = "../asm/samples/exec_destination.asm"

	intermediateMarkerImm = "#0x494E" // "IN"
	sentinelImm           = "#0x534E" // "SN"
	destinationMarkerImm  = "#0x4453" // "DS"

	intermediateMarkerSlotImm = "#0x0100"
	sentinelSlotImm           = "#0x0104"
	destinationMarkerSlotImm  = "#0x0108"

	// Marker slots start at 0x0100, so both samples must stay below it.
	markerSlotsStart = 0x0100

	// F2-01 reserves [control+0x000, control+0x100) for the bootstrap.
	bootstrapMaxBytes = 256
)

// loadSampleInstructions parses a sample without assembling it, so tests can
// inspect the order and operands of the instructions.
func loadSampleInstructions(t *testing.T, path string) [][]string {
	t.Helper()

	instructions, err := LoadAssemblyFile(path)
	if err != nil {
		t.Fatalf("failed to parse %s: %v", path, err)
	}
	if len(instructions) == 0 {
		t.Fatalf("%s produced no instructions", path)
	}

	return instructions
}

// findInstruction returns the index of the first instruction matching the
// opcode and every operand, or -1 when there is no match.
func findInstruction(instructions [][]string, opcode string, operands ...string) int {
	for index, instruction := range instructions {
		if len(instruction) != len(operands)+1 {
			continue
		}
		if !strings.EqualFold(instruction[0], opcode) {
			continue
		}

		matches := true
		for operandIndex, operand := range operands {
			if !strings.EqualFold(instruction[operandIndex+1], operand) {
				matches = false
				break
			}
		}
		if matches {
			return index
		}
	}

	return -1
}

func TestExecIntermediateSample(t *testing.T) {
	memory, err := RunAssembler(execIntermediateSample)
	if err != nil {
		t.Fatalf("failed to assemble %s: %v", execIntermediateSample, err)
	}
	if len(memory) == 0 {
		t.Fatalf("%s assembled to an empty image", execIntermediateSample)
	}
	if len(memory)%4 != 0 {
		t.Fatalf("image size %d is not a whole number of 32-bit instructions", len(memory))
	}
	if len(memory) > markerSlotsStart {
		t.Fatalf("code uses %d bytes and overlaps the marker slots at 0x%04X", len(memory), markerSlotsStart)
	}

	instructions := loadSampleInstructions(t, execIntermediateSample)

	syscallIndex := findInstruction(instructions, "SYSCALL", "#5")
	if syscallIndex < 0 {
		t.Fatal("intermediate sample does not issue SYSCALL #5")
	}

	// The ImageID is a full 32-bit value in W8. Any earlier use of W8 would
	// either clobber it or move it into a 16-bit immediate.
	for index, instruction := range instructions[:syscallIndex] {
		for _, operand := range instruction[1:] {
			if strings.EqualFold(operand, "W8") {
				t.Fatalf("W8 is used before SYSCALL #5 at instruction %d: %v", index, instruction)
			}
		}
	}

	markerIndex := findInstruction(instructions, "MV", "W0", intermediateMarkerImm)
	if markerIndex < 0 || markerIndex >= syscallIndex {
		t.Fatalf("intermediate marker must be set before SYSCALL #5, got index %d", markerIndex)
	}
	markerStoreIndex := findInstruction(instructions, "STRD", "W0", intermediateMarkerSlotImm)
	if markerStoreIndex < 0 || markerStoreIndex >= syscallIndex {
		t.Fatalf("intermediate marker must be published before SYSCALL #5, got index %d", markerStoreIndex)
	}

	sentinelIndex := findInstruction(instructions, "MV", "W1", sentinelImm)
	if sentinelIndex <= syscallIndex {
		t.Fatalf("sentinel must be set after SYSCALL #5, got index %d", sentinelIndex)
	}
	sentinelStoreIndex := findInstruction(instructions, "STRD", "W1", sentinelSlotImm)
	if sentinelStoreIndex <= sentinelIndex {
		t.Fatalf("sentinel must be published after it is set, got index %d", sentinelStoreIndex)
	}

	// The failure path must terminate explicitly instead of falling through.
	exitIndex := findInstruction(instructions, "SYSCALL", "#2")
	if exitIndex <= sentinelStoreIndex {
		t.Fatalf("failure path must exit after the sentinel, got index %d", exitIndex)
	}
	if statusIndex := findInstruction(instructions, "MV", "W8", "#1"); statusIndex <= syscallIndex {
		t.Fatalf("exit status must be set only after the exec call, got index %d", statusIndex)
	}
}

func TestExecDestinationSample(t *testing.T) {
	memory, err := RunAssembler(execDestinationSample)
	if err != nil {
		t.Fatalf("failed to assemble %s: %v", execDestinationSample, err)
	}
	if len(memory) == 0 {
		t.Fatalf("%s assembled to an empty image", execDestinationSample)
	}
	if len(memory)%4 != 0 {
		t.Fatalf("image size %d is not a whole number of 32-bit instructions", len(memory))
	}
	if len(memory) > markerSlotsStart {
		t.Fatalf("code uses %d bytes and overlaps the marker slots at 0x%04X", len(memory), markerSlotsStart)
	}

	instructions := loadSampleInstructions(t, execDestinationSample)

	// No _start label is used: the first emitted instruction is the entry point
	// at virtual address zero, and the assembler already proved that no label is
	// required.
	first := instructions[0]
	if len(first) != 3 || !strings.EqualFold(first[0], "MV") ||
		!strings.EqualFold(first[1], "W0") || !strings.EqualFold(first[2], destinationMarkerImm) {
		t.Fatalf("destination must start at offset zero with its marker, got %v", first)
	}

	markerStoreIndex := findInstruction(instructions, "STRD", "W0", destinationMarkerSlotImm)
	if markerStoreIndex <= 0 {
		t.Fatalf("destination marker must be published after entry, got index %d", markerStoreIndex)
	}

	// W1 and W2 are read by the loop, so both must be initialized first. This
	// is what makes the image safe when it starts with zeroed or stale
	// registers.
	initW1 := findInstruction(instructions, "MV", "W1", "#0")
	initW2 := findInstruction(instructions, "MV", "W2", "#1")
	addIndex := findInstruction(instructions, "ADD", "W1", "W1", "W2")
	if initW1 < 0 || initW2 < 0 || addIndex < 0 {
		t.Fatalf("destination loop is incomplete: W1=%d W2=%d ADD=%d", initW1, initW2, addIndex)
	}
	if initW1 > addIndex || initW2 > addIndex {
		t.Fatal("destination loop reads W1/W2 before initializing them")
	}

	// A backwards JUMP keeps the process in a stable, inspectable loop.
	loopIndex := -1
	for index, instruction := range instructions {
		if len(instruction) != 2 || !strings.EqualFold(instruction[0], "JUMP") {
			continue
		}
		offset, parseErr := strconv.Atoi(instruction[1])
		if parseErr == nil && offset < 0 {
			loopIndex = index
			break
		}
	}
	if loopIndex <= addIndex {
		t.Fatalf("destination must loop back after the counter update, got index %d", loopIndex)
	}
}

func TestExecSampleMarkersAreDistinct(t *testing.T) {
	values := []struct {
		name  string
		value string
	}{
		{"intermediate marker", intermediateMarkerImm},
		{"sentinel", sentinelImm},
		{"destination marker", destinationMarkerImm},
	}
	seenValues := make(map[string]string, len(values))
	for _, marker := range values {
		if previous, exists := seenValues[marker.value]; exists {
			t.Fatalf("%s reuses the value %s from %s", marker.name, marker.value, previous)
		}
		seenValues[marker.value] = marker.name
	}

	slots := []struct {
		name  string
		value string
	}{
		{"intermediate marker slot", intermediateMarkerSlotImm},
		{"sentinel slot", sentinelSlotImm},
		{"destination marker slot", destinationMarkerSlotImm},
	}
	seenSlots := make(map[string]string, len(slots))
	for _, slot := range slots {
		if previous, exists := seenSlots[slot.value]; exists {
			t.Fatalf("%s reuses address %s from %s", slot.name, slot.value, previous)
		}
		seenSlots[slot.value] = slot.name
	}
}

func TestExecSamplesUseExistingOpcodes(t *testing.T) {
	for _, path := range []string{execIntermediateSample, execDestinationSample} {
		instructions := loadSampleInstructions(t, path)
		for index, instruction := range instructions {
			opcode := strings.ToUpper(instruction[0])
			if _, exists := opcodeMap[opcode]; !exists {
				t.Fatalf("%s instruction %d uses unsupported opcode %q", path, index, opcode)
			}
		}
	}
}

func TestBootstrapAssemblyFitsControlSegment(t *testing.T) {
	// F2-04 pins the 256-byte budget of the dedicated control bootstrap
	// segment. The runtime producer is F2-16; this fixture proves that a
	// bootstrap can be host-assembled with the existing syntax and fits.
	const bootstrapSource = `
_start:
    MV W8, #0      ; exit status
    SYSCALL #2     ; exit()
`

	memory, err := RunAssemblerFromReader(bytes.NewBufferString(bootstrapSource))
	if err != nil {
		t.Fatalf("failed to assemble bootstrap fixture: %v", err)
	}
	if len(memory) == 0 {
		t.Fatal("bootstrap fixture assembled to an empty image")
	}
	if len(memory)%4 != 0 {
		t.Fatalf("bootstrap size %d is not a whole number of 32-bit instructions", len(memory))
	}
	if len(memory) > bootstrapMaxBytes {
		t.Fatalf("bootstrap uses %d bytes, exceeding the %d-byte control segment", len(memory), bootstrapMaxBytes)
	}
}
