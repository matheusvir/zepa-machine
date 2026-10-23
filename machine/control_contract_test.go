package machine

import (
	"encoding/binary"
	"errors"
	"reflect"
	"testing"
	"unsafe"
)

func TestControlStructureTableAlignmentAndBounds(t *testing.T) {
	segments := []struct {
		name         string
		offset, size uint32
	}{
		{"bootstrap", ControlBootstrapOffset, ControlBootstrapSize},
		{"status", ControlStatusOffset, ControlStatusSize},
		{"descriptor", ControlImageDescriptorOffset, ControlImageDescriptorSize},
		{"request", ControlRequestOffset, ControlRequestSize},
		{"result", ControlResultOffset, ControlResultSize},
		{"event", ControlEventOffset, ControlEventSize},
	}
	for i, segment := range segments {
		if segment.offset%ControlWordBytes != 0 || segment.size%ControlWordBytes != 0 {
			t.Errorf("%s is not word aligned", segment.name)
		}
		if segment.offset+segment.size > uint32(ControlRegionEnd-ControlRegionStart) {
			t.Errorf("%s exceeds control region", segment.name)
		}
		if i > 0 && segments[i-1].offset+segments[i-1].size > segment.offset {
			t.Errorf("%s overlaps %s", segments[i-1].name, segment.name)
		}
	}
	if ControlUsedBytes != 0x1A0 || ControlBootstrapSize != 256 || ControlMagic != 0x46324331 || ControlProtocolVersion != 1 {
		t.Fatal("bootstrap/control v1 constants changed")
	}
	if ControlStatusOffset != 0x100 || ControlImageDescriptorOffset != 0x120 || ControlRequestOffset != 0x140 || ControlResultOffset != 0x160 || ControlEventOffset != 0x180 {
		t.Fatal("control record offsets changed")
	}
}

func TestControlWireWordEncodingAndEnums(t *testing.T) {
	word := make([]byte, 4)
	binary.LittleEndian.PutUint32(word, ControlMagic)
	if got := [4]byte(word); got != [4]byte{0x31, 0x43, 0x32, 0x46} {
		t.Fatalf("magic encoding = %x", got)
	}
	if RequestKindNone != 0 || RequestLoad != 1 || RequestInit != 2 || RequestExec != 3 ||
		ResultKindNone != 0 || ResultLoadOK != 1 || ResultLoadFailed != 2 || ResultInitCreated != 3 || ResultInitFailed != 4 || ResultExecSucceeded != 5 || ResultExecFailed != 6 ||
		EventKindNone != 0 || EventInitCreated != 1 || EventInitFailed != 2 || EventExecStarted != 3 || EventExecSucceeded != 4 || EventExecFailed != 5 ||
		PhaseUninitialized != 0 || PhaseReady != 1 || PhaseLoading != 2 || PhaseInitializing != 3 || PhaseExecuting != 4 || PhaseFailed != 5 ||
		ReasonNone != 0 || ReasonInvalidImage != 1 || ReasonImageUnavailable != 2 || ReasonNoFrames != 3 || ReasonNoPCB != 4 || ReasonInvalidLayout != 5 {
		t.Fatal("v1 enum values changed")
	}
	if TerminationCodeInsufficientFrames != 3 || TerminationCodeInvalidReference != 4 {
		t.Fatal("specified termination codes changed")
	}
}

func TestIdentityTypesAndZeroInvalid(t *testing.T) {
	var image ImageID
	var request RequestID
	var token PublicationToken
	if image != 0 || request != 0 || token != 0 {
		t.Fatal("zero values must represent unset identity/token")
	}
	var physical PhysicalAddress
	var virtual VirtualAddress
	if physical != 0 || virtual != 0 {
		t.Fatal("address domains must have independent zero values")
	}
}

func TestImageLengthBoundariesAndDescriptorPrefix(t *testing.T) {
	if MaxImagePayloadBytes != 65532 || ImageLengthPrefixBytes != 4 || TransferBufferBytes != 65536 {
		t.Fatalf("payload limits: max=%d prefix=%d buffer=%d", MaxImagePayloadBytes, ImageLengthPrefixBytes, TransferBufferBytes)
	}
	tests := []struct {
		name    string
		length  uint32
		wantErr bool
	}{
		{"zero is invalid", 0, true},
		{"maximum accepted", 65532, false},
		{"one over maximum", 65533, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateImagePayloadLength(test.length)
			if (err != nil) != test.wantErr {
				t.Fatalf("ValidateImagePayloadLength(%d) error = %v", test.length, err)
			}
		})
	}
	var prefix [ImageLengthPrefixBytes]byte
	binary.LittleEndian.PutUint32(prefix[:], 12)
	var descriptorWords [ControlImageDescriptorSize]byte
	binary.LittleEndian.PutUint32(descriptorWords[DescriptorLengthOffset:], 12)
	prefixedLength := binary.LittleEndian.Uint32(prefix[:])
	descriptorLength := binary.LittleEndian.Uint32(descriptorWords[DescriptorLengthOffset:])
	if err := ValidateImageDescriptorLength(descriptorLength, prefixedLength); err != nil {
		t.Fatalf("matching length prefix rejected: %v", err)
	}
	binary.LittleEndian.PutUint32(descriptorWords[DescriptorLengthOffset:], 11)
	descriptorLength = binary.LittleEndian.Uint32(descriptorWords[DescriptorLengthOffset:])
	if err := ValidateImageDescriptorLength(descriptorLength, prefixedLength); !errors.Is(err, ErrDescriptorLengthMismatch) {
		t.Fatalf("mismatching prefix error = %v", err)
	}
	binary.LittleEndian.PutUint32(prefix[:], 65533)
	binary.LittleEndian.PutUint32(descriptorWords[DescriptorLengthOffset:], 65533)
	prefixedLength = binary.LittleEndian.Uint32(prefix[:])
	descriptorLength = binary.LittleEndian.Uint32(descriptorWords[DescriptorLengthOffset:])
	if err := ValidateImageDescriptorLength(descriptorLength, prefixedLength); !errors.Is(err, ErrInvalidImageLength) {
		t.Fatalf("oversized prefix error = %v", err)
	}
}

func TestControlRecordStructSizesAndFieldOffsets(t *testing.T) {
	if unsafe.Sizeof(ControlBootstrap{}) != uintptr(ControlBootstrapSize) {
		t.Errorf("ControlBootstrap size = %d, want %d", unsafe.Sizeof(ControlBootstrap{}), ControlBootstrapSize)
	}
	if unsafe.Sizeof(ControlStatus{}) != uintptr(ControlStatusSize) {
		t.Errorf("ControlStatus size = %d, want %d", unsafe.Sizeof(ControlStatus{}), ControlStatusSize)
	}
	if unsafe.Sizeof(ImageDescriptor{}) != uintptr(ControlImageDescriptorSize) ||
		unsafe.Sizeof(ControlRequest{}) != uintptr(ControlRequestSize) ||
		unsafe.Sizeof(ControlResult{}) != uintptr(ControlResultSize) ||
		unsafe.Sizeof(ControlEvent{}) != uintptr(ControlEventSize) {
		t.Fatal("mailbox record struct size differs from wire size")
	}
	if unsafe.Offsetof(ControlBootstrap{}.Magic) != uintptr(BootstrapMagicOffset) ||
		unsafe.Offsetof(ControlBootstrap{}.Version) != uintptr(BootstrapVersionOffset) ||
		unsafe.Offsetof(ControlBootstrap{}.Capabilities) != uintptr(BootstrapCapabilitiesOffset) ||
		unsafe.Offsetof(ControlBootstrap{}.Ready) != uintptr(BootstrapReadyOffset) ||
		unsafe.Offsetof(ControlBootstrap{}.ControlBasePhysical) != uintptr(BootstrapControlPhysicalOffset) ||
		unsafe.Offsetof(ControlBootstrap{}.ImageBasePhysical) != uintptr(BootstrapImagePhysicalOffset) ||
		unsafe.Offsetof(ControlBootstrap{}.BufferBasePhysical) != uintptr(BootstrapBufferPhysicalOffset) {
		t.Fatal("bootstrap struct field offsets differ from wire offsets")
	}
	if unsafe.Offsetof(ControlStatus{}.Version) != uintptr(StatusVersionOffset) ||
		unsafe.Offsetof(ControlStatus{}.Phase) != uintptr(StatusPhaseOffset) ||
		unsafe.Offsetof(ControlStatus{}.Ready) != uintptr(StatusReadyOffset) ||
		unsafe.Offsetof(ControlStatus{}.Reserved) != uintptr(StatusReservedOffset) ||
		unsafe.Offsetof(ImageDescriptor{}.Length) != uintptr(DescriptorLengthOffset) ||
		unsafe.Offsetof(ImageDescriptor{}.ValidToken) != uintptr(DescriptorValidTokenOffset) ||
		unsafe.Offsetof(ImageDescriptor{}.AckToken) != uintptr(DescriptorAckTokenOffset) ||
		unsafe.Offsetof(ControlRequest{}.RequestID) != uintptr(RequestIDOffset) ||
		unsafe.Offsetof(ControlResult{}.TerminationCode) != uintptr(ResultTerminationCodeOffset) ||
		unsafe.Offsetof(ControlEvent{}.Reserved) != uintptr(EventReservedOffset) {
		t.Fatal("control struct field offsets differ from wire offsets")
	}
}

func TestPublicationTokenAndRequestIDAreDistinctTypes(t *testing.T) {
	if reflect.TypeOf(RequestID(0)) == reflect.TypeOf(PublicationToken(0)) || reflect.TypeOf(PhysicalAddress(0)) == reflect.TypeOf(VirtualAddress(0)) {
		t.Fatal("identity and address domains must remain distinct named types")
	}
}
