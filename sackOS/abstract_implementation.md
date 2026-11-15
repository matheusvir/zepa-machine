# sackOS Abstract Implementation

## setup
```
store memory_size

set UPTR and KPTR

allocate the first frame to the first page
allocate all kernel frames to high pages

ESR = 32 //enable MMU
EPC = next instruction
MRET

jump to high addresses
unmap low pages

populate the first 1GB worth of frames of the bitmap

SP = SP_ADDRESS
ESA = EXCEPTION_SUPERVISOR_ADDRESS
EPC = LOOP ADDRESS
ESR = 48 //will enable interruptions and MMU
MRET //go to infinite loop, waiting for exceptions
```

# Interruption handlers

## exception_supervisor()
```
Save W0 and W1 into scratch space
if ecr == clock_int:
  clock_int()

if running_pid != -1
  Save all registers on pcb_v[running_pid] //remember W0 and W1 in scratch space
Jump to the specific handler based on the ECR
```

## clock_int() 
```
clock_interrupt_count++
if clock_interrupt_count < TIME_SLICE:
  load W0 and W1 back from scratch space
  MRET

clock_interrupt_count = 0

if running_pid == -1:
  load W0 and W1 back from scratch space
  MRET

Save all registers on pcb_v[running_pid] //remember W0 and W1 in scratch space

schedule()
```

## input_int()
```
Extract the size from buffer

for pid in range(0, MAX_PROCESSES):
  if pcb_v[pid].is_mapped = 0:
    //initialize the PCB
    pcb_v[pid].is_mapped = 1
    pcb_v[pid].is_zombie = 0
    pcb_v[pid].is_waiting = 0
    pcb_v[pid].scheduler_state = ready
    pcb_v[pid].parent_pid = -1
    pcb_v[pid].child = -1
    pcb_v[pid].next_sibling = -1
    pcb_v[pid].prev_sibling = -1
    pcb_v[pid].status_addr = -1
    pcb_v[pid].pages_used = 0

    SP = 3GB (user virtual memory limit)
    //All other registers are 0
    load pid UPTR
    for page_id in range(0, buffer_size/4KB):
      x = map_page(page_id)
      if x != 0:
        kill(pid)
    Copy the buffer (BUFFER bytes) to the user memory
    break

schedule()
```

## kill_int()
```
pid = buffer
if pid >= MAX_PROCESSES or pcb_v[pid].is_mapped = 0 or pcb_v[pid].is_zombie = 1:
  schedule()

pcb_v[pid].w9 = 2
kill(pid)
```

## syscall_int()
```
Jumps to specific handler based on W9
```

## fault_int()
```
pcb_v[running_pid].w9 = 1
kill(running_pid)
```

## page_fault_exc()
```
  if efa >= kernelBoundary and esr not in kernel mode:
    pcb_v[running_pid].w9 = 3
    kill(running_pid)

  page_id calculated based on efa
  x = map_page(page_id)
  if x != 0:
    pcb_v[running_pid].w9 = 3
    kill(running_pid)

  clear page with all 0
  
  restore CPU context of running_pid
  MRET //make sure to make EPC point to the instruction before what it currently is
```

# Syscall handlers

## fork() - ID 0
```
for pid in range(0, MAX_PROCESSES):
  if pcb_v[pid].is_mapped = 0:
    //initialize the PCB
    pcb_v[pid].is_mapped = 1
    pcb_v[pid].is_zombie = 0
    pcb_v[pid].is_waiting = 0
    pcb_v[pid].scheduler_state = ready
    pcb_v[pid].parent_pid = running_pid
    pcb_v[pid].child = -1
    pcb_v[pid].next_sibling = -1
    pcb_v[pid].prev_sibling = -1
    pcb_v[pid].status_addr = -1
    pcb_v[pid].pages_used = 0
    
    
    pcb_v[running_pid].w9 = pid
    pcb_v[pid].w9 = -2

    pcb_v[pid].next_sibling = pcb_v[running_pid].child
    if pcb_v[running_pid].child != -1:
      pcb_v[pcb_v[running_pid].child].prev_sibling = pid
    pcb_v[running_pid].child = pid

    //All other registers are copied from pcb_v[running_pid] 
    load pid UPTR
    for pte in pcb_v[running_pid].page_table:
      if pte.Valid:
        // chante the uptr to the child one
        x = map_page(pageId)
        if x != 0:
          kill(pid)
        copy the page content from pte.page to the child page (switch between parent PTR and child PTR to copy)
      
    schedule()

pcb_v[running_pid].w9 = -1
schedule()
```

## wait(status_addr) - ID 1
```
if status_addr >= 0xC0000000 || status_addr page is not valid:
  fault_int()

if pcb_v[running_pid].child == -1:
  pcb_v[running_pid].w9 = -1
  schedule()
else:
  curr_child = pcb_v[running_pid].child
  do:
    if pcb_v[curr_child].is_zombie:
      pcb_v[curr_child].is_mapped = 0
      pcb_v[running_pid].w9 = curr_child
      running_pid_virtual_memory[status_addr] = pcb_v[curr_child].w9

      if pcb_v[running_pid].child = curr_child:
        pcb_v[running_pid].child = pcb_v[curr_child].next_sibling
      else:
        pcb_v[pcb_v[curr_child].prev_sibling].next_sibling = pcb_v[curr_child].next_sibling

      if pcb_v[curr_child].next_sibling != -1:
        pcb_v[pcb_v[curr_child].next_sibling].prev_sibling = pcb_v[curr_child].prev_sibling
      
      schedule()

    else:
      curr_child = pcb_v[curr_child].next_sibling

  while curr_child != -1

  pcb_v[running_pid].status_addr = status_addr
  pcb_v[running_pid].scheduler_state = blocked
  pcb_v[running_pid].is_waiting = 1
  schedule()
```

## exit(status_code) - ID 2
```
pcb_v[running_pid].w9 = status_code
kill(running_pid)
```

## getPID() - ID 3
```
pcb[running_pid].w9 = running_pid
schedule() 
```

## rele() - ID 4
```
schedule()
```

# Helper functions

## kill(pid)
**pid is on W9 (unchanged entry contract)**

Cleanup runs first, before any lifecycle effect. W0 keeps the pid across the
helper call, because W9 becomes the helper result.
```
SP = kernel_stack_pointer   // the interrupted SP is already saved in the PCB
x = page_release(&pcb_v[pid], &pcb_v[pid].page_table)
if x != 0:
  kill_cleanup_panic        // W9 = 5, W0 = pid; nothing released or changed
W9 = pid

parent = pcb_v[pid].parent_pid
if parent != -1:
  if pcb_v[parent].is_waiting:
    pcb_v[parent].is_waiting = 0
    pcb_v[parent].scheduler_state = ready
    pcb_v[parent].w9 = pid
    memory[pcb_v[parent].status_addr] = pcb_v[pid].w9

    if pcb_v[parent].child = pid:
      pcb_v[parent].child = pcb_v[pid].next_sibling

    if pcb_v[pid].prev_sibling != -1:
      pcb_v[pcb_v[pid].prev_sibling].next_sibling = pcb_v[pid].next_sibling

    if pcb_v[pid].next_sibling != -1:
      pcb_v[pcb_v[pid].next_sibling].prev_sibling = pcb_v[pid].prev_sibling

    pcb_v[pid].is_mapped = 0
  
  else:
    pcb_v[pid].is_zombie = 1

else:
  pcb_v[pid].is_mapped = 0

curr_child = pcb_v[pid].child
while curr_child != -1:
  pcb_v[curr_child].parent_pid = -1
  pcb_v[curr_child].prev_sibling = -1
  next = pcb_v[curr_child].next_sibling
  pcb_v[curr_child].next_sibling = -1
  curr_child = next

schedule()
```

`kill_cleanup_panic` is a labeled self-jump reached only from this branch.
W9 = 5 means the kernel's page accounting is already corrupt (a kernel bug or
an external RAM write), so the kernel stops instead of scheduling. kill runs
in exception context with interrupts disabled, so the CPU stays at the label
until the machine is restarted. The target's PTEs, bitmap bits, pages_used,
flags, family links and the parent's status/readiness are left unchanged for
inspection.

## page_release(pcb, page_table)
**W7 = target PCB kernel virtual address**
**W6 = target page-table kernel virtual address (must be W7 + 80)**
**return address is on W8 (return with JMPR W8)**
**return on W9: 0 if every user page was released, 5 if invalid layout, count or frame**

Returning helper: it never calls kill or schedule and does not depend on
running_pid or on the active UPTR, so it can clean a nonrunning or partially
built process.

Execution preconditions: kernel mode, MMU enabled, interrupts disabled,
SP pointing to a mapped kernel stack with at least 48 free bytes below it, and
no concurrent writer to the target table or the bitmap.

Preserved: W0-W4, W8, K0, K1, UPTR, KPTR, SP, ECR, ESA, ESR, EPC, EFA and the
SR bits other than the comparison flags. Clobbered: W5, W6, W7, W9 and the
comparison flags. Process identity, family links, flags, saved registers,
status memory and scheduling state are never written.

Stack: the prologue pushes W0-W4, K0, K1 and reserves 5 local words (48 bytes
in total, descending); every return pops the same 48 bytes.

Traversal: both phases walk the table in blocks of 4 PTEs from both ends (low
block [low, low+3], high block [high-3, high]). Because the table has
0xC0000 = 8 * 0x18000 entries, the two blocks never overlap and the pointers
cross exactly, so every PTE is visited once per phase. A block is skipped when
the OR of its 4 PTEs has no valid bit; otherwise its entries are handled one by
one. The alias scan skips blocks the same way.
```
if pcb is not the exact base of a slot < min(MAX_PROCESSES, 256)
   or page_table != pcb + 80:
  return 5

// phase 1: read-only preflight, low and high blocks over all 0xC0000 PTEs
valid = 0
low = 0, high = 0xBFFFF
while low <= high:              // unsigned; blocks never overlap, stop on crossing
  if any pte[low..low+3] is valid: check each of them
  if any pte[high-3..high] is valid: check each of them
  low += 4, high -= 4

check(i): if pte[i] is valid:
  valid++
  frame = pte[i].frame
  if frame >= frame_count (memory_size / 4KB, 0 means 4GB): return 5
  if frame < 0x40000 (reserved, covers every kernel mapping): return 5
  if bitmap[frame] == 0 (already free): return 5
  if some pte[j], j > i, is valid with the same frame (alias): return 5

if valid != pages_used: return 5     // both directions, also 0 with valid PTEs

// phase 2: release, same bounded block traversal
release(i): if pte[i] is valid:
  if pages_used == 0: return 5       // unexpected after preflight, no underflow
  bitmap[frame] &= ~(1 << bit)        // clears only this bit
  pte[i] = 0
  pages_used = pages_used - 1        // persisted at pcb + 76

if pages_used != 0: return 5
return 0
```
A detected failure in phase 1 leaves PTEs, bitmap and pages_used unchanged.
Frames shared with another process are not detected: the allocator never
creates them (map_page only hands out free frames and fork copies user pages).
The alias check costs at most pages_used * 0xC0000 extra PTE reads.

## schedule()
```
clock_interrupt_count = 0

if running_pid >= MAX_PROCESSES:
  running_pid = 0

if pcb_v[running_pid].scheduler_state = running:
  pcb_v[running_pid].scheduler_state = ready

limit_pid = running_pid+1
if limit_pid = MAX_PROCESSES:
  limit_pid = 0

curr_pid = limit_pid

do:
  if pcb_v[curr_pid].is_mapped = 1 and pcb_v[curr_pid].is_zombie = 0 and pcb_v[curr_pid].scheduler_state = ready:
    pcb_v[curr_pid].scheduler_state = running
    running_pid = curr_pid
    load every register of pcb_v[running_pid] into the cpu 
    UPTR = pcb_v[running_pid].page_table address
    mret
  
  curr_pid++
  if curr_pid == MAX_PROCESSES:
    curr_pid = 0

while curr_pid != limit_pid

running_pid = -1
EPC = LOOP ADDRESS
ESR = 48 //will enable interruptions
MRET //go to infinite loop, waiting for exceptions
```

## map_page(page_id)
**page_id is on W9**
**return address is on W8**
**return on W9, 0 if successful, 1 otherwise**
```
search the bitmap for a free frame, map the PTE of page_id to the free frame, set valid = 1
if it is an user page:
  update pages_used (using address before uptr)

if there is no free frame:
  if efa >= kernelBoundary:
    panic
  w9 = 1

w9 = 0
jump back to return address
```


# Data Structures
- Each Page has the fixed size of 4KB
- PTE has 32bit 
- The virtual memory is divided in 3GB(3/4) to the user and 1GB(1/4) for the kernel
- There will be at most MAX_PROCESSES process stored on memory
- Each PID will be defined as a 4 byte unsigned integer

### Constants (set by the OS developer) (32 bits)

- **MAX_PROCESSES**: maximum number of processes (normally defaulted to 256 processes)
- **TIME_SLICE**: defined as the amount of clock interrupts to trigger the scheduler
- **BUFFER_SIZE**: size of the input buffer 

### Singular values (32 bits)
- **memory_size**: how much memory is there available
- **running_pid**: PID of the current running process, or -1 if no process is running
- **clock_interrupt_count**: number of clock interruptions since last scheduler call
- **kernel_stack_pointer**: points to the current kernel stack
- **scratch_space_0**: aux address to temporarily save W0 on interruptions
- **scratch_space_1**: aux address to temporarily save W1 on interruptions

### PCB
- **parent_pid**: 4 bytes [0]
- **child**: 4 bytes [4]
- **prev_sibling**: 4 bytes [8]
- **next_sibling**: 4 bytes [12]
- **status_addr**: 4 bytes [16]
- **registers**: 13 registers, 4 bytes each, 52 bytes total
  - **W0** [20]
  - **W1** [24]
  - **W2** [28]
  - **W3** [32]
  - **W4** [36]
  - **W5** [40]
  - **W6** [44]
  - **W7** [48]
  - **W8** [52]
  - **W9** [56]
  - **PC** [60]
  - **SP** [64]
  - **SR** [68]
- **flags**: (1 byte) [72]
  - **is_mapped**: 1 bit [0] (informs if this position in the PCB vector correspond to a process)
  - **is_zombie**: 1 bit [1]
  - **is_waiting**: 1 bit [2]
  - **scheduler_state (running, ready, blocked)**: 2 bits [3:4]
  **Pages_Used**: 4 bytes [76]
  **PAGE TABLE**: (3 MB) [80]
  - 3*2^18 PTEs, each PTE has 4 bytes
- **total_size**: 3MB + 80 bytes = 3145808 bytes 

### Kernel Page Table

2^18 PTEs = 2^18*4 bytes = 2^20 bytes

### Bitmap

Array that will tell whether a page frame is being used or not [2^20 bits = 128 KB]

### Queue

Similar to the Xv6 scheduler, the PCB vector is the queue itself, with the scheduler iterating it continuously until it finds a ready process.

# Addresses
The kernel code has approximately 5KB, for simplicity purposes, there will be 8KB dedicated for the kernel's code. 
Take caution when using these addresses, when using virutal kernel addresses, these values need to be offset by the user memory size (3 GB).

### Constants (set by the OS developer) (32 bits)
- **MAX_PROCESSES**: 0x2000
- **TIME_SLICE**: 0x2004
- **BUFFER_SIZE**: 0x2008

### Singular values (32 bits)
- **memory_size**: 0x200C
- **running_pid**: 0x2010
- **clock_interrupt_count**: 0x2014
- **kernel_stack_pointer**: 0x2018
- **scratch_space_0**: 0x201C
- **scratch_space_1**: 0x2020

### Data Structures
- **pcb_vector**: 0x2024
- **kernel_page_table**: 0x30007024
- **bitmap**: 0x30107024
(these addresses assume at most 256 processes at a time)