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
	if ControlUsedBytes != 0x300 || ControlBootstrapSize != 256 || ControlMagic != 0x46324331 || ControlProtocolVersion != 1 {
		t.Fatal("bootstrap/control v1 constants changed")
	}
	if ControlReservedGapOffset != 0x1A0 || ControlReservedGapSize != 0x60 || ControlExecutableBytesOffset != 0x200 || ControlExecutableBytesSize != 256 || ZEPAInstructionBytes != 4 {
		t.Fatal("reserved gap or executable bootstrap range changed")
	}
	if ControlReservedGapOffset+ControlReservedGapSize != ControlExecutableBytesOffset ||
		ControlExecutableBytesOffset%ZEPAInstructionBytes != 0 ||
		ControlExecutableBytesOffset+ControlExecutableBytesSize > uint32(ControlRegionEnd-ControlRegionStart) {
		t.Fatal("executable bootstrap alignment or control-region bounds changed")
	}
	if ControlStatusOffset != 0x100 || ControlImageDescriptorOffset != 0x120 || ControlRequestOffset != 0x140 || ControlResultOffset != 0x160 || ControlEventOffset != 0x180 {
		t.Fatal("control record offsets changed")
	}
}

func TestExecutableBootstrapLengthBoundaries(t *testing.T) {
	tests := []struct {
		length  uint32
		wantErr bool
	}{
		{0, true},
		{1, true},
		{3, true},
		{4, false},
		{256, false},
		{260, true},
	}
	for _, test := range tests {
		if err := ValidateExecutableBootstrapLength(test.length); (err != nil) != test.wantErr {
			t.Errorf("ValidateExecutableBootstrapLength(%d) error = %v", test.length, err)
		}
	}
}

func TestBootstrapWireBytesAndReservedPadding(t *testing.T) {
	var control [ControlUsedBytes]byte
	binary.LittleEndian.PutUint32(control[BootstrapExecutableLengthOffset:], 4)
	if got := binary.LittleEndian.Uint32(control[0x1C:]); got != 4 {
		t.Fatalf("encoded executable length = %d", got)
	}
	if unsafe.Offsetof(ControlBootstrap{}.ExecutableLength) != uintptr(BootstrapExecutableLengthOffset) || BootstrapExecutableLengthOffset != 0x1C {
		t.Fatalf("executable length field offset = %d", unsafe.Offsetof(ControlBootstrap{}.ExecutableLength))
	}
	for i, value := range control[0x20:ControlBootstrapSize] {
		if value != 0 {
			t.Fatalf("reserved bootstrap byte %#x is nonzero", i+0x20)
		}
	}
	copy(control[ControlExecutableBytesOffset:], []byte{0x10, 0x20, 0x30, 0x40})
	for i, value := range control[ControlReservedGapOffset:ControlExecutableBytesOffset] {
		if value != 0 {
			t.Fatalf("reserved control gap byte %#x is nonzero", uint32(i)+ControlReservedGapOffset)
		}
	}
	for i, value := range control[ControlExecutableBytesOffset+4 : ControlExecutableBytesOffset+ControlExecutableBytesSize] {
		if value != 0 {
			t.Fatalf("unused executable byte %#x is nonzero", uint32(i)+ControlExecutableBytesOffset+4)
		}
	}
	if got := binary.LittleEndian.Uint32(control[ControlExecutableBytesOffset:]); got != 0x40302010 {
		t.Fatalf("executable bytes fixture word = %#x", got)
	}
	if ControlExecutableBytesOffset+ControlExecutableBytesSize != ControlUsedBytes {
		t.Fatal("executable bootstrap does not end at ControlUsedBytes")
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
		ControlStatusOffset+StatusReadyOffset != 0x108 || ControlStatusOffset+StatusReadyOffset == BootstrapReadyOffset ||
		unsafe.Offsetof(ControlStatus{}.Reserved) != uintptr(StatusReservedOffset) ||
		unsafe.Offsetof(ImageDescriptor{}.Length) != uintptr(DescriptorLengthOffset) ||
		unsafe.Offsetof(ImageDescriptor{}.ValidToken) != uintptr(DescriptorValidTokenOffset) ||
		unsafe.Offsetof(ImageDescriptor{}.AckToken) != uintptr(DescriptorAckTokenOffset) ||
		unsafe.Offsetof(ImageDescriptor{}.Valid) != uintptr(DescriptorValidOffset) ||
		unsafe.Offsetof(ImageDescriptor{}.Reserved) != uintptr(DescriptorReservedOffset) ||
		unsafe.Offsetof(ControlRequest{}.RequestID) != uintptr(RequestIDOffset) ||
		unsafe.Offsetof(ControlResult{}.TerminationCode) != uintptr(ResultTerminationCodeOffset) ||
		unsafe.Offsetof(ControlEvent{}.Reserved) != uintptr(EventReservedOffset) {
		t.Fatal("control struct field offsets differ from wire offsets")
	}
}

func TestDescriptorValidityLifecycleFixture(t *testing.T) {
	// This byte-array fixture documents protocol state transitions only. It is
	// not a loader, queue, or production lifecycle implementation.
	var descriptor [ControlImageDescriptorSize]byte
	imageWindow := []byte{4, 0, 0, 0, 0xAA, 0xBB, 0xCC, 0xDD}
	originalImage := append([]byte(nil), imageWindow...)
	pinnedReader := true
	binary.LittleEndian.PutUint32(descriptor[DescriptorImageIDOffset:], 21)
	binary.LittleEndian.PutUint32(descriptor[DescriptorLengthOffset:], 4)
	binary.LittleEndian.PutUint32(descriptor[DescriptorGenerationOffset:], 7)
	binary.LittleEndian.PutUint32(descriptor[DescriptorValidTokenOffset:], 12)
	binary.LittleEndian.PutUint32(descriptor[DescriptorAckTokenOffset:], 11)
	binary.LittleEndian.PutUint32(descriptor[DescriptorValidOffset:], ImageDescriptorValid)
	nextGeneration := uint32(8)
	if binary.LittleEndian.Uint32(descriptor[DescriptorValidOffset:]) != ImageDescriptorValid {
		t.Fatal("descriptor validity must not depend on whether its token is acknowledged")
	}
	// An already-acknowledged descriptor still requires and retains Valid=1.
	acknowledgedDescriptor := descriptor
	binary.LittleEndian.PutUint32(acknowledgedDescriptor[DescriptorAckTokenOffset:], 12)
	if got := binary.LittleEndian.Uint32(acknowledgedDescriptor[DescriptorValidOffset:]); got != ImageDescriptorValid {
		t.Fatalf("descriptor ack changed lifecycle Valid: %d", got)
	}

	// Admission immediately invalidates the old image but cannot reset or
	// appropriate the metadata publication tokens.
	failedAttemptGeneration := nextGeneration
	nextGeneration++
	binary.LittleEndian.PutUint32(descriptor[DescriptorValidOffset:], ImageDescriptorInvalid)
	if got := binary.LittleEndian.Uint32(descriptor[DescriptorValidOffset:]); got != ImageDescriptorInvalid {
		t.Fatalf("admission Valid = %d", got)
	}
	if got := binary.LittleEndian.Uint32(descriptor[DescriptorValidTokenOffset:]); got != 12 {
		t.Fatalf("admission changed valid token: %d", got)
	}
	if got := binary.LittleEndian.Uint32(descriptor[DescriptorAckTokenOffset:]); got != 11 {
		t.Fatalf("admission changed ack token: %d", got)
	}
	if got := binary.LittleEndian.Uint32(descriptor[DescriptorGenerationOffset:]); got != 7 {
		t.Fatalf("admission changed successful generation: %d", got)
	}
	if got := binary.LittleEndian.Uint32(descriptor[DescriptorImageIDOffset:]); got != 21 {
		t.Fatalf("admission changed descriptor metadata before ack: %d", got)
	}
	if validToken, ackToken := binary.LittleEndian.Uint32(descriptor[DescriptorValidTokenOffset:]), binary.LittleEndian.Uint32(descriptor[DescriptorAckTokenOffset:]); validToken == ackToken {
		t.Fatal("fixture expected the prior descriptor metadata to remain pending")
	}
	// Failure leaves Valid=0 and preserves the most recent successful generation.
	if got := binary.LittleEndian.Uint32(descriptor[DescriptorValidOffset:]); got != 0 || failedAttemptGeneration != 8 {
		t.Fatalf("failed load state Valid=%d consumed generation=%d", got, failedAttemptGeneration)
	}
	if got := binary.LittleEndian.Uint32(descriptor[DescriptorGenerationOffset:]); got != 7 {
		t.Fatalf("failed attempt replaced successful generation: %d", got)
	}

	// Ack the prior metadata publication; it still does not modify lifecycle
	// Valid, and only now may descriptor metadata be reused.
	binary.LittleEndian.PutUint32(descriptor[DescriptorAckTokenOffset:], 12)
	if got := binary.LittleEndian.Uint32(descriptor[DescriptorValidOffset:]); got != ImageDescriptorInvalid {
		t.Fatalf("ack changed an invalid descriptor's lifecycle Valid: %d", got)
	}
	if got := binary.LittleEndian.Uint32(descriptor[DescriptorAckTokenOffset:]); got != 12 {
		t.Fatalf("ack of prior descriptor token = %d", got)
	}
	if pinnedReader && !reflect.DeepEqual(imageWindow, originalImage) {
		t.Fatal("admitted failed attempt changed bytes held by a pinned reader")
	}
	pinnedReader = false
	if pinnedReader {
		t.Fatal("fixture reader should have released before overwrite")
	}
	// A second admitted attempt consumes a new generation, leaving a gap
	// because failed attempt generation 8 is retired.
	successGeneration := nextGeneration
	nextGeneration++
	if successGeneration != 9 {
		t.Fatalf("successful attempt generation = %d, want gap after failure", successGeneration)
	}

	copy(imageWindow, make([]byte, len(imageWindow)))
	binary.LittleEndian.PutUint32(imageWindow, 4)
	copy(imageWindow[ImageLengthPrefixBytes:], []byte{1, 2, 3, 4})
	publicationOrder := []string{"image-prefix-and-payload"}
	binary.LittleEndian.PutUint32(descriptor[DescriptorImageIDOffset:], 22)
	publicationOrder = append(publicationOrder, "image-id")
	binary.LittleEndian.PutUint32(descriptor[DescriptorLengthOffset:], 4)
	publicationOrder = append(publicationOrder, "length")
	binary.LittleEndian.PutUint32(descriptor[DescriptorGenerationOffset:], successGeneration)
	publicationOrder = append(publicationOrder, "generation")
	binary.LittleEndian.PutUint32(descriptor[DescriptorValidTokenOffset:], 13)
	publicationOrder = append(publicationOrder, "valid-token")
	binary.LittleEndian.PutUint32(descriptor[DescriptorValidOffset:], ImageDescriptorValid)
	publicationOrder = append(publicationOrder, "lifecycle-valid")
	if publicationOrder[len(publicationOrder)-1] != "lifecycle-valid" {
		t.Fatal("descriptor Valid was not the final commit store")
	}
	if got := binary.LittleEndian.Uint32(descriptor[DescriptorValidOffset:]); got != 1 {
		t.Fatalf("successful commit Valid = %d", got)
	}
	if got := binary.LittleEndian.Uint32(descriptor[DescriptorGenerationOffset:]); got != 9 {
		t.Fatalf("successful descriptor generation = %d", got)
	}
	if got := binary.LittleEndian.Uint32(descriptor[DescriptorImageIDOffset:]); got != 22 {
		t.Fatalf("successful descriptor ImageID = %d", got)
	}
	if got := binary.LittleEndian.Uint32(imageWindow); got != binary.LittleEndian.Uint32(descriptor[DescriptorLengthOffset:]) {
		t.Fatalf("committed prefix=%d descriptor length=%d", got, binary.LittleEndian.Uint32(descriptor[DescriptorLengthOffset:]))
	}
	if got := binary.LittleEndian.Uint32(descriptor[DescriptorAckTokenOffset:]); got != 12 {
		t.Fatalf("publication unexpectedly changed ack token: %d", got)
	}
	if got := binary.LittleEndian.Uint32(descriptor[DescriptorValidTokenOffset:]); got != 13 {
		t.Fatalf("successful metadata publication token = %d", got)
	}
	if got := binary.LittleEndian.Uint32(descriptor[DescriptorReservedOffset:]); got != 0 {
		t.Fatalf("descriptor reserved word = %d", got)
	}
}

func TestPublicationTokenAndRequestIDAreDistinctTypes(t *testing.T) {
	if reflect.TypeOf(RequestID(0)) == reflect.TypeOf(PublicationToken(0)) || reflect.TypeOf(PhysicalAddress(0)) == reflect.TypeOf(VirtualAddress(0)) {
		t.Fatal("identity and address domains must remain distinct named types")
	}
}
