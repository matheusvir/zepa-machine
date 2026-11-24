package machine

import (
	"encoding/binary"
	"testing"
)

// F2-07 stage 5: full-tier control records on the real 1 GiB + 4 KiB layout.
//
// The compact fixture relocates KPTR and the bitmap. This tier runs the real
// setup through newASMSetupRun, so KPTR is 0x30009024 and the bitmap is
// 0x30109024. The helpers below only put raw control words; they do not add a
// mailbox token state machine.

func newFullTier(t *testing.T) *Machine {
	t.Helper()
	r := newASMSetupRun(t)
	r.runSetup()
	if got := r.m.registers[kptr]; got != 0x30009024 {
		t.Fatalf("full-tier KPTR = %#x, want #0x30009024", got)
	}
	if got := uint32(BitmapStart); got != 0x30109024 {
		t.Fatalf("full-tier bitmap = %#x, want #0x30109024", got)
	}
	if got := physWord(r.m, asmBitmap); got != ^uint32(0) {
		t.Fatalf("full-tier bitmap word = %#x, want populated", got)
	}
	return r.m
}

func putFullControl(m *Machine, offset, value uint32) {
	binary.LittleEndian.PutUint32(m.memory[uint32(ControlRegionStart)+offset:], value)
}

// putFullBootstrap writes the host bootstrap fields and publishes Ready last.
func putFullBootstrap(m *Machine, version, controlBase, imageBase, bufferBase uint32) {
	off := ControlBootstrapOffset
	putFullControl(m, off+BootstrapMagicOffset, ControlMagic)
	putFullControl(m, off+BootstrapVersionOffset, version)
	putFullControl(m, off+BootstrapCapabilitiesOffset, ControlCapabilitiesV1)
	putFullControl(m, off+BootstrapControlPhysicalOffset, controlBase)
	putFullControl(m, off+BootstrapImagePhysicalOffset, imageBase)
	putFullControl(m, off+BootstrapBufferPhysicalOffset, bufferBase)
	putFullControl(m, off+BootstrapExecutableLengthOffset, ZEPAInstructionBytes)
	putFullControl(m, off+BootstrapReadyOffset, 1)
}

// putFullDescriptor writes descriptor metadata and publishes Valid last.
func putFullDescriptor(m *Machine, imageID, length, base, generation, validToken, ackToken uint32) {
	off := ControlImageDescriptorOffset
	putFullControl(m, off+DescriptorImageIDOffset, imageID)
	putFullControl(m, off+DescriptorLengthOffset, length)
	putFullControl(m, off+DescriptorPhysicalBaseOffset, base)
	putFullControl(m, off+DescriptorGenerationOffset, generation)
	putFullControl(m, off+DescriptorValidTokenOffset, validToken)
	putFullControl(m, off+DescriptorAckTokenOffset, ackToken)
	putFullControl(m, off+DescriptorValidOffset, ImageDescriptorValid)
}

// putFullRecord writes consecutive raw words at a control slot base.
func putFullRecord(m *Machine, base uint32, words ...uint32) {
	for i, word := range words {
		putFullControl(m, base+uint32(i)*ControlWordBytes, word)
	}
}

// putFullImage writes the image-window length prefix and payload.
func putFullImage(t *testing.T, m *Machine, image []byte) {
	t.Helper()
	if len(image) == 0 || uint32(len(image)) > MaxImagePayloadBytes {
		t.Fatalf("image is %d bytes, want 1..%d", len(image), MaxImagePayloadBytes)
	}
	binary.LittleEndian.PutUint32(m.memory[uint32(ImageWindowStart):], uint32(len(image)))
	copy(m.memory[uint32(ImageWindowStart)+ImageLengthPrefixBytes:], image)
}

// TestKernelControlFullTierRecords checks the real addresses and exact words of
// the bootstrap, descriptor, slots, image window and 64 KiB transfer buffer.
func TestKernelControlFullTierRecords(t *testing.T) {
	m := newFullTier(t)
	putFullBootstrap(m, ControlProtocolVersion, uint32(ControlRegionStart), uint32(ImageWindowStart), uint32(TransferBufferStart))
	putFullDescriptor(m, 0x1234, 8, uint32(ImageWindowStart)+ImageLengthPrefixBytes, 0x5678, 3, 2)
	putFullRecord(m, ControlRequestOffset, uint32(RequestExec), 0x1234, 7, 0, 5, 4, 0x99, 0)
	putFullRecord(m, ControlResultOffset, uint32(ResultExecSucceeded), 0, 7, 0x1234, 0x99, 6, 5, 0)
	putFullRecord(m, ControlEventOffset, uint32(EventExecSucceeded), 0, 7, 0x1234, 0x99, 8, 7, 0)
	putFullImage(t, m, []byte{0x11, 0x22, 0x33, 0x44})
	payload := []byte{0xAA, 0xBB, 0xCC, 0xDD}
	m.writeTransferBufferLocked(payload)

	control := uint32(ControlRegionStart)
	checks := []struct {
		name string
		addr uint32
		want uint32
	}{
		{"bootstrap.magic", control + ControlBootstrapOffset + BootstrapMagicOffset, ControlMagic},
		{"bootstrap.version", control + ControlBootstrapOffset + BootstrapVersionOffset, ControlProtocolVersion},
		{"bootstrap.control_base", control + ControlBootstrapOffset + BootstrapControlPhysicalOffset, control},
		{"bootstrap.image_base", control + ControlBootstrapOffset + BootstrapImagePhysicalOffset, uint32(ImageWindowStart)},
		{"bootstrap.buffer_base", control + ControlBootstrapOffset + BootstrapBufferPhysicalOffset, uint32(TransferBufferStart)},
		{"bootstrap.executable_length", control + ControlBootstrapOffset + BootstrapExecutableLengthOffset, ZEPAInstructionBytes},
		{"bootstrap.ready", control + ControlBootstrapOffset + BootstrapReadyOffset, 1},
		{"descriptor.image_id", control + ControlImageDescriptorOffset + DescriptorImageIDOffset, 0x1234},
		{"descriptor.length", control + ControlImageDescriptorOffset + DescriptorLengthOffset, 8},
		{"descriptor.base", control + ControlImageDescriptorOffset + DescriptorPhysicalBaseOffset, uint32(ImageWindowStart) + ImageLengthPrefixBytes},
		{"descriptor.generation", control + ControlImageDescriptorOffset + DescriptorGenerationOffset, 0x5678},
		{"descriptor.valid_token", control + ControlImageDescriptorOffset + DescriptorValidTokenOffset, 3},
		{"descriptor.ack_token", control + ControlImageDescriptorOffset + DescriptorAckTokenOffset, 2},
		{"descriptor.valid_last", control + ControlImageDescriptorOffset + DescriptorValidOffset, ImageDescriptorValid},
		{"request.kind", control + ControlRequestOffset + RequestKindOffset, uint32(RequestExec)},
		{"request.valid_token", control + ControlRequestOffset + RequestValidTokenOffset, 5},
		{"request.ack_token", control + ControlRequestOffset + RequestAckTokenOffset, 4},
		{"request.id", control + ControlRequestOffset + RequestIDOffset, 0x99},
		{"result.kind", control + ControlResultOffset + ResultKindOffset, uint32(ResultExecSucceeded)},
		{"result.request_id", control + ControlResultOffset + ResultRequestIDOffset, 0x99},
		{"result.valid_token", control + ControlResultOffset + ResultValidTokenOffset, 6},
		{"result.ack_token", control + ControlResultOffset + ResultAckTokenOffset, 5},
		{"event.kind", control + ControlEventOffset + EventKindOffset, uint32(EventExecSucceeded)},
		{"event.request_id", control + ControlEventOffset + EventRequestIDOffset, 0x99},
		{"event.valid_token", control + ControlEventOffset + EventValidTokenOffset, 8},
		{"event.ack_token", control + ControlEventOffset + EventAckTokenOffset, 7},
		{"image.length", uint32(ImageWindowStart), 4},
		{"image.first", uint32(ImageWindowStart) + ImageLengthPrefixBytes, 0x44332211},
		{"buffer.length", uint32(TransferBufferStart), uint32(len(payload))},
		{"buffer.first", uint32(TransferBufferStart) + ImageLengthPrefixBytes, 0xDDCCBBAA},
	}
	for _, check := range checks {
		if got := physWord(m, check.addr); got != check.want {
			t.Fatalf("%s at %#x = %#x, want %#x", check.name, check.addr, got, check.want)
		}
	}
	if uint32(ImageWindowStart) != 0x3ffe0000 || TransferBufferBytes != 64*1024 || uint32(TransferBufferStart) != 0x3fff0000 {
		t.Fatalf("image/buffer = %#x and %d bytes at %#x, want #0x3ffe0000 and 64 KiB at #0x3fff0000",
			ImageWindowStart, TransferBufferBytes, TransferBufferStart)
	}
}

// TestKernelControlFullTierSafePoint mirrors the safe_point_test protocol gates
// after host magic is published on the real setup machine.
func TestKernelControlFullTierSafePoint(t *testing.T) {
	m := newFullTier(t)
	if !m.atSafePointLocked() {
		t.Fatal("setup without control magic must remain a legacy safe point")
	}
	putFullBootstrap(m, ControlProtocolVersion, uint32(ControlRegionStart), uint32(ImageWindowStart), uint32(TransferBufferStart))
	if !m.atSafePointLocked() {
		t.Fatal("published magic with ready status must be a safe point")
	}

	putFullControl(m, ControlStatusOffset+StatusReadyOffset, 0)
	if m.atSafePointLocked() {
		t.Fatal("safe point before kernel status.ready")
	}
	putFullControl(m, ControlStatusOffset+StatusReadyOffset, 1)
	for _, phase := range []ControlPhase{PhaseLoading, PhaseInitializing, PhaseExecuting} {
		putFullControl(m, ControlStatusOffset+StatusPhaseOffset, uint32(phase))
		if m.atSafePointLocked() {
			t.Fatalf("safe point while guest operation is in phase %d", phase)
		}
	}
	putFullControl(m, ControlStatusOffset+StatusPhaseOffset, uint32(PhaseReady))
	putFullControl(m, ControlResultOffset+ResultValidTokenOffset, 2)
	putFullControl(m, ControlResultOffset+ResultAckTokenOffset, 1)
	if m.atSafePointLocked() {
		t.Fatal("safe point while terminal result awaits ack")
	}
	putFullControl(m, ControlResultOffset+ResultAckTokenOffset, 2)
	if !m.atSafePointLocked() {
		t.Fatal("ready protocol with acknowledged result must be a safe point")
	}
	m.registers[sr] &^= 16
	if m.atSafePointLocked() {
		t.Fatal("safe point with interrupts disabled")
	}
}
