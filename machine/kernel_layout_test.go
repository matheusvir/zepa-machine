package machine

import "testing"

func TestKernelLayoutArithmeticAndDisjointRegions(t *testing.T) {
	regions := []struct {
		name       string
		start, end PhysicalAddress
	}{
		{"code", KernelCodeStart, KernelCodeEnd},
		{"globals", KernelGlobalsStart, KernelGlobalsEnd},
		{"pcbs", PCBVectorStart, PCBVectorEnd},
		{"page table", KernelPageTableStart, KernelPageTableEnd},
		{"bitmap", BitmapStart, BitmapEnd},
		{"stack", KernelStackStart, KernelStackEnd},
		{"control", ControlRegionStart, ControlRegionEnd},
		{"image", ImageWindowStart, ImageWindowEnd},
		{"buffer", TransferBufferStart, TransferBufferEnd},
	}
	for i, region := range regions {
		if region.start >= region.end {
			t.Errorf("%s has invalid bounds [%#x,%#x)", region.name, region.start, region.end)
		}
		if i > 0 && regions[i-1].end > region.start {
			t.Errorf("%s overlaps %s", regions[i-1].name, region.name)
		}
	}
	if got := uint32(PCBVectorStart) + PCBCount*PCBStride; got != uint32(PCBVectorEnd) {
		t.Fatalf("PCB vector end = %#x", got)
	}
	if uint32(PCBVectorEnd) != 0x30009024 {
		t.Fatalf("PCB vector boundary = %#x", PCBVectorEnd)
	}
	lastPCB, ok := PCBAddress(PCBCount - 1)
	if !ok || uint32(lastPCB)+PCBStride != uint32(PCBVectorEnd) {
		t.Fatalf("last PCB does not end at vector boundary: %#x", lastPCB)
	}
	if uint32(KernelPageTableEnd)-uint32(KernelPageTableStart) != 0x100000 || uint32(BitmapEnd)-uint32(BitmapStart) != 0x20000 {
		t.Fatal("page table or bitmap span differs from contract")
	}
	if _, ok := PCBAddress(PCBCount); ok {
		t.Fatal("out-of-range PID accepted")
	}
	v, ok := ToKernelVirtual(KernelStackEnd)
	if !ok || v != 0xFFFD0000 {
		t.Fatalf("virtual stack top = %#x, valid=%t", v, ok)
	}
	if _, ok := ToKernelVirtual(PhysicalMemoryEnd); ok {
		t.Fatal("physical end must not be treated as a mapped byte")
	}
	if PhysicalMemoryBytes != 0x40000000 || RunnerMinimumBackingBytes != 0x40001000 || RunnerMaximumBackingBytes != 0x100000000 {
		t.Fatal("physical capacity or runner backing range changed")
	}
}

func TestGlobalAddressesAndPreservedPCBOffsets(t *testing.T) {
	got := []PhysicalAddress{
		GlobalAddress(GlobalMaxProcessesOffset), GlobalAddress(GlobalTimeSliceOffset),
		GlobalAddress(GlobalBufferSizeOffset), GlobalAddress(GlobalMemorySizeOffset),
		GlobalAddress(GlobalRunningPIDOffset), GlobalAddress(GlobalClockCountOffset),
		GlobalAddress(GlobalKernelStackPointerOffset), GlobalAddress(GlobalScratch0Offset),
		GlobalAddress(GlobalScratch1Offset),
	}
	want := []PhysicalAddress{0x4000, 0x4004, 0x4008, 0x400C, 0x4010, 0x4014, 0x4018, 0x401C, 0x4020}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("global %d = %#x, want %#x", i, got[i], want[i])
		}
	}
	if PCBParentOffset != 0 || PCBChildOffset != 4 || PCBPreviousSiblingOffset != 8 || PCBNextSiblingOffset != 12 || PCBStatusAddressOffset != 16 ||
		PCBW0Offset != 20 || PCBW9Offset != 56 || PCBProgramCounterOffset != 60 || PCBStackPointerOffset != 64 || PCBStatusRegisterOffset != 68 ||
		PCBFlagsOffset != 72 || PCBPagesUsedOffset != 76 || PCBPageTableOffset != 80 || PCBByteSize != 0x300050 {
		t.Fatal("PCB field contract changed")
	}
}
