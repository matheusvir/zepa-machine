package machine

import (
	"context"
	"errors"
)

// The shared control map is v1, little-endian, and consists exclusively of
// aligned uint32 words. Offsets are relative to ControlRegionStart.
const (
	ControlProtocolVersion       uint32 = 1
	ControlWordBytes             uint32 = 4
	ControlBootstrapOffset       uint32 = 0x000
	ControlBootstrapSize         uint32 = 0x100
	ControlStatusOffset          uint32 = 0x100
	ControlStatusSize            uint32 = 0x020
	ControlImageDescriptorOffset uint32 = 0x120
	ControlImageDescriptorSize   uint32 = 0x020
	ControlRequestOffset         uint32 = 0x140
	ControlRequestSize           uint32 = 0x020
	ControlResultOffset          uint32 = 0x160
	ControlResultSize            uint32 = 0x020
	ControlEventOffset           uint32 = 0x180
	ControlEventSize             uint32 = 0x020
	ControlUsedBytes             uint32 = 0x1A0
	ControlMagic                 uint32 = 0x46324331
	ControlCapabilitiesV1        uint32 = 0
	ImageLengthPrefixBytes       uint32 = 4
	ImageWindowBytes             uint32 = uint32(ImageWindowEnd - ImageWindowStart)
	MaxImagePayloadBytes         uint32 = ImageWindowBytes - ImageLengthPrefixBytes
	TransferBufferBytes          uint32 = uint32(TransferBufferEnd - TransferBufferStart)
	MaxPendingEvents             uint32 = 1
)

const (
	BootstrapMagicOffset           uint32 = 0x00
	BootstrapVersionOffset         uint32 = 0x04
	BootstrapCapabilitiesOffset    uint32 = 0x08
	BootstrapReadyOffset           uint32 = 0x0C
	BootstrapControlPhysicalOffset uint32 = 0x10
	BootstrapImagePhysicalOffset   uint32 = 0x14
	BootstrapBufferPhysicalOffset  uint32 = 0x18

	StatusVersionOffset  uint32 = 0x00
	StatusPhaseOffset    uint32 = 0x04
	StatusReadyOffset    uint32 = 0x08
	StatusReservedOffset uint32 = 0x0C

	DescriptorImageIDOffset      uint32 = 0x00
	DescriptorLengthOffset       uint32 = 0x04
	DescriptorPhysicalBaseOffset uint32 = 0x08
	DescriptorGenerationOffset   uint32 = 0x0C
	DescriptorValidTokenOffset   uint32 = 0x10
	DescriptorAckTokenOffset     uint32 = 0x14

	RequestKindOffset       uint32 = 0x00
	RequestImageIDOffset    uint32 = 0x04
	RequestPIDOffset        uint32 = 0x08
	RequestFlagsOffset      uint32 = 0x0C
	RequestValidTokenOffset uint32 = 0x10
	RequestAckTokenOffset   uint32 = 0x14
	RequestIDOffset         uint32 = 0x18

	ResultKindOffset            uint32 = 0x00
	ResultReasonOffset          uint32 = 0x04
	ResultPIDOffset             uint32 = 0x08
	ResultImageIDOffset         uint32 = 0x0C
	ResultRequestIDOffset       uint32 = 0x10
	ResultValidTokenOffset      uint32 = 0x14
	ResultAckTokenOffset        uint32 = 0x18
	ResultTerminationCodeOffset uint32 = 0x1C

	EventKindOffset       uint32 = 0x00
	EventReasonOffset     uint32 = 0x04
	EventPIDOffset        uint32 = 0x08
	EventImageIDOffset    uint32 = 0x0C
	EventRequestIDOffset  uint32 = 0x10
	EventValidTokenOffset uint32 = 0x14
	EventAckTokenOffset   uint32 = 0x18
	EventReservedOffset   uint32 = 0x1C
)

// RequestKind identifies load, init, or exec ingress.
type RequestKind uint32

const (
	RequestKindNone RequestKind = iota
	RequestLoad
	RequestInit
	RequestExec
)

// ResultKind identifies the durable outcome published for a request.
type ResultKind uint32

const (
	ResultKindNone ResultKind = iota
	ResultLoadOK
	ResultLoadFailed
	ResultInitCreated
	ResultInitFailed
	ResultExecSucceeded
	ResultExecFailed
)

// EventKind identifies lifecycle notifications independently of results.
type EventKind uint32

const (
	EventKindNone EventKind = iota
	EventInitCreated
	EventInitFailed
	EventExecStarted
	EventExecSucceeded
	EventExecFailed
)

// ControlPhase describes protocol or operation phase; zero is uninitialized.
type ControlPhase uint32

const (
	PhaseUninitialized ControlPhase = iota
	PhaseReady
	PhaseLoading
	PhaseInitializing
	PhaseExecuting
	PhaseFailed
)

// DiagnosticReason is a protocol-level cause; zero means no diagnostic.
type DiagnosticReason uint32

const (
	ReasonNone DiagnosticReason = iota
	ReasonInvalidImage
	ReasonImageUnavailable
	ReasonNoFrames
	ReasonNoPCB
	ReasonInvalidLayout
)

const (
	TerminationCodeInsufficientFrames uint32 = 3
	TerminationCodeInvalidReference   uint32 = 4
)

// Bootstrap and all following records describe their exact wire-word order.
// Reserved bootstrap words through byte 0xFF are zero in v1.
type ControlBootstrap struct {
	Magic, Version, Capabilities, Ready                        uint32
	ControlBasePhysical, ImageBasePhysical, BufferBasePhysical uint32
	Reserved                                                   [57]uint32
}

// ControlStatus occupies 0x100..0x11F. Ready is published after initialization.
type ControlStatus struct {
	Version      uint32
	Phase        ControlPhase
	Ready        uint32
	Reserved     uint32
	ReservedTail [4]uint32
}

// ImageDescriptor occupies 0x120..0x13F; tokens are distinct from ImageID.
type ImageDescriptor struct {
	ImageID      ImageID
	Length       uint32
	PhysicalBase PhysicalAddress
	Generation   uint32
	ValidToken   PublicationToken
	AckToken     PublicationToken
	Reserved     [2]uint32
}

// ControlRequest occupies 0x140..0x15F. V1 flags are zero.
type ControlRequest struct {
	Kind       RequestKind
	ImageID    ImageID
	PID        uint32
	Flags      uint32
	ValidToken PublicationToken
	AckToken   PublicationToken
	RequestID  RequestID
	Reserved   uint32
}

// ControlResult occupies 0x160..0x17F and is durable through its ack.
type ControlResult struct {
	Kind            ResultKind
	Reason          DiagnosticReason
	PID             uint32
	ImageID         ImageID
	RequestID       RequestID
	ValidToken      PublicationToken
	AckToken        PublicationToken
	TerminationCode uint32
}

// ControlEvent occupies 0x180..0x19F and is independently acknowledged.
type ControlEvent struct {
	Kind       EventKind
	Reason     DiagnosticReason
	PID        uint32
	ImageID    ImageID
	RequestID  RequestID
	ValidToken PublicationToken
	AckToken   PublicationToken
	Reserved   uint32
}

// ValidateImagePayloadLength is a pure bounds check suitable for ingress
// validation; it neither assembles nor stores an image.
func ValidateImagePayloadLength(length uint32) error {
	if length == 0 || length > MaxImagePayloadBytes {
		return ErrInvalidImageLength
	}
	return nil
}

// ValidateImageDescriptorLength checks both the image prefix and descriptor
// metadata; a descriptor cannot describe a differently sized payload.
func ValidateImageDescriptorLength(descriptorLength, prefixedLength uint32) error {
	if err := ValidateImagePayloadLength(prefixedLength); err != nil {
		return err
	}
	if descriptorLength != prefixedLength {
		return ErrDescriptorLengthMismatch
	}
	return nil
}

var (
	ErrInvalidImageLength       = errors.New("machine: invalid image payload length")
	ErrDescriptorLengthMismatch = errors.New("machine: descriptor length does not match image prefix")
)

// UnavailableImageError reports an absent or already retired ImageID.
type UnavailableImageError struct{ ImageID ImageID }

func (e *UnavailableImageError) Error() string { return "machine: image unavailable" }

// InvalidLayoutError reports a malformed or unsupported shared-memory layout.
type InvalidLayoutError struct{ Detail string }

func (e *InvalidLayoutError) Error() string { return "machine: invalid control layout: " + e.Detail }

// BusySlotError reports that the single publication slot is still unacked.
type BusySlotError struct {
	Slot  string
	Token PublicationToken
}

func (e *BusySlotError) Error() string { return "machine: publication slot busy: " + e.Slot }

// ImageTransferIngress defines host-to-ZEPA staging via the existing input
// buffer path. It does not define image assembly or storage behavior.
type ImageTransferIngress interface {
	TransferImage(context.Context, []byte) error
}

// ControlInspector exposes read-only bootstrap and status inspection. It does
// not initialize or mutate either record.
type ControlInspector interface {
	ReadControlState(context.Context) (ControlBootstrap, ControlStatus, error)
}

// ImageDescriptorTransport exposes descriptor metadata and explicit ack.
type ImageDescriptorTransport interface {
	ReadImageDescriptor(context.Context) (ImageDescriptor, error)
	AckImageDescriptor(context.Context, PublicationToken) error
}

// RequestSubmitter allocates the RequestID before publishing and returns it.
type RequestSubmitter interface {
	Submit(context.Context, ControlRequest) (RequestID, error)
}

// RequestTransport exposes the serialized ingress slot to its kernel consumer.
type RequestTransport interface {
	ReadRequest(context.Context) (ControlRequest, error)
	AckRequest(context.Context, PublicationToken) error
}

// ResultTransport reads durable results and explicitly acknowledges the slot.
type ResultTransport interface {
	ReadResult(context.Context) (ControlResult, error)
	AckResult(context.Context, PublicationToken) error
}

// EventTransport reads lifecycle events and explicitly acknowledges the slot.
type EventTransport interface {
	ReadEvent(context.Context) (ControlEvent, error)
	AckEvent(context.Context, PublicationToken) error
}
