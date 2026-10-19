package machine

// PhysicalAddress and VirtualAddress are distinct 32-bit address domains.
type PhysicalAddress uint32
type VirtualAddress uint32

// ImageID is opaque and session-unique; it is never a process identifier.
type ImageID uint32

// RequestID correlates one logical operation, not a mailbox publication.
type RequestID uint32

// PublicationToken identifies one publication in one independently acked slot.
type PublicationToken uint32

const (
	KernelVirtualOffset       uint32          = 0xC0000000
	PhysicalMemoryEnd         PhysicalAddress = 0x40000000
	PhysicalMemoryBytes       uint32          = 1 << 30
	RunnerMinimumBackingBytes uint32          = PhysicalMemoryBytes + 0x1000
	RunnerMaximumBackingBytes uint64          = 1 << 32

	KernelCodeStart      PhysicalAddress = 0x00000000
	KernelCodeEnd        PhysicalAddress = 0x00004000
	KernelGlobalsStart   PhysicalAddress = 0x00004000
	KernelGlobalsEnd     PhysicalAddress = 0x00004024
	PCBVectorStart       PhysicalAddress = 0x00004024
	PCBVectorEnd         PhysicalAddress = 0x30009024
	KernelPageTableStart PhysicalAddress = 0x30009024
	KernelPageTableEnd   PhysicalAddress = 0x30109024
	BitmapStart          PhysicalAddress = 0x30109024
	BitmapEnd            PhysicalAddress = 0x30129024
	KernelStackStart     PhysicalAddress = 0x3FFC0000
	KernelStackEnd       PhysicalAddress = 0x3FFD0000
	ControlRegionStart   PhysicalAddress = 0x3FFD0000
	ControlRegionEnd     PhysicalAddress = 0x3FFE0000
	ImageWindowStart     PhysicalAddress = 0x3FFE0000
	ImageWindowEnd       PhysicalAddress = 0x3FFF0000
	TransferBufferStart  PhysicalAddress = 0x3FFF0000
	TransferBufferEnd    PhysicalAddress = 0x40000000

	PCBCount       uint32 = 256
	PCBStride      uint32 = 0x300050
	PCBByteSize    uint32 = PCBStride
	PCBVectorBytes uint32 = PCBCount * PCBStride

	GlobalMaxProcessesOffset       uint32 = 0x00
	GlobalTimeSliceOffset          uint32 = 0x04
	GlobalBufferSizeOffset         uint32 = 0x08
	GlobalMemorySizeOffset         uint32 = 0x0C
	GlobalRunningPIDOffset         uint32 = 0x10
	GlobalClockCountOffset         uint32 = 0x14
	GlobalKernelStackPointerOffset uint32 = 0x18
	GlobalScratch0Offset           uint32 = 0x1C
	GlobalScratch1Offset           uint32 = 0x20

	PCBParentOffset          uint32 = 0
	PCBChildOffset           uint32 = 4
	PCBPreviousSiblingOffset uint32 = 8
	PCBNextSiblingOffset     uint32 = 12
	PCBStatusAddressOffset   uint32 = 16
	PCBW0Offset              uint32 = 20
	PCBW9Offset              uint32 = 56
	PCBProgramCounterOffset  uint32 = 60
	PCBStackPointerOffset    uint32 = 64
	PCBStatusRegisterOffset  uint32 = 68
	PCBFlagsOffset           uint32 = 72
	PCBPagesUsedOffset       uint32 = 76
	PCBPageTableOffset       uint32 = 80
)

// GlobalAddress returns a physical address for one of the nine global words.
func GlobalAddress(offset uint32) PhysicalAddress {
	return PhysicalAddress(uint32(KernelGlobalsStart) + offset)
}

// PCBAddress returns the start of a PCB when pid is within the fixed vector.
func PCBAddress(pid uint32) (PhysicalAddress, bool) {
	if pid >= PCBCount {
		return 0, false
	}
	return PhysicalAddress(uint32(PCBVectorStart) + pid*PCBStride), true
}

// ToKernelVirtual translates a physical address in the kernel-mapped region.
func ToKernelVirtual(addr PhysicalAddress) (VirtualAddress, bool) {
	if addr >= PhysicalMemoryEnd || uint64(addr)+uint64(KernelVirtualOffset) > 0xFFFFFFFF {
		return 0, false
	}
	return VirtualAddress(uint32(addr) + KernelVirtualOffset), true
}
