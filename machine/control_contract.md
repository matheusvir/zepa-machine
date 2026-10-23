# Front 2 shared layout and control contract (v1)

This document and the declarations in `kernel_layout.go` and
`control_contract.go` are definitions only. They do not implement a loader,
queue, controller, image store, init/exec service, handler, or operational
stub. The addresses are the planned layout, not a claim that the existing
kernel or debugger has migrated to it. This contract is scoped to F2-01 issue
#17; it does not replace the project's syscall/argv source of truth.

## Physical layout and identities

Physical regions are half-open intervals. Reserved gaps remain reserved and
are not part of any payload.

| Region | Physical range |
|---|---|
| Kernel code | `[0x00000000, 0x00004000)` |
| Nine global words | `[0x00004000, 0x00004024)` |
| 256-entry PCB vector | `[0x00004024, 0x30009024)` |
| Kernel page table (KPTR) | `[0x30009024, 0x30109024)` |
| Bitmap | `[0x30109024, 0x30129024)` |
| Kernel stack | `[0x3FFC0000, 0x3FFD0000)` |
| Control map | `[0x3FFD0000, 0x3FFE0000)` |
| Protected image window | `[0x3FFE0000, 0x3FFF0000)` |
| Transfer/staging buffer | `[0x3FFF0000, 0x40000000)` |

All kernel virtual mappings add `0xC0000000` to a mapped physical address.
Thus the physical stack end maps to virtual `0xFFFD0000`. That virtual value
does not imply physical backing at or above 4 GiB. `0x40000000` is the
1-GiB physical layout end. The runner's backing range is currently at least
1 GiB + 4 KiB and at most 4 GiB; every consumer must check actual physical
backing before indexing. The 256 PCBs have stride `0x300050`; PID indexes
this vector, never an image. PCB relative offsets stay unchanged: parent 0,
child 4, previous sibling 8, next sibling 12, status address 16, W0–W9
20–56, PC 60, SP 64, SR 68, flags 72, pages-used 76, page table 80.

The nine global words, in order, are max-processes, time-slice, buffer-size,
memory-size, running PID, clock count, kernel SP, scratch0, scratch1, at
`0x4000` through `0x4020` in four-byte increments. The old addresses in
`kernel.asm`, `debug.go`, `main_kernel.go`, and
`abstract_implementation.md` are downstream migration dependencies; this
definition does not edit or activate those consumers.

| Existing consumer | Existing assumption to migrate in a later issue |
|---|---|
| `sackOS/kernel.asm` | PCB base `0x2024`, KPTR `0x30007024`, bitmap derived from old KPTR; initial SP is `0x00000000 - BUFFER_SIZE` (virtual `0xFFFF0000` for 64 KiB), not the new stack top `0xFFFD0000`. |
| `machine/debug.go` | PCB base `0x2024`, KPTR `0x30007024`, bitmap `0x30107024`; PCB stride/field offsets stay the same. |
| `main_kernel.go` | Initializes nine-globals predecessor addresses `0x2000`, `0x2004`, `0x2008` and accepts physical backing from 1 GiB + 4 KiB through 4 GiB. |
| `sackOS/abstract_implementation.md` | Documents globals `0x2000..0x2020`, PCB base `0x2024`, KPTR `0x30007024`, bitmap `0x30107024`. |

`PhysicalAddress`, `VirtualAddress`, `ImageID`, `RequestID`, and
`PublicationToken` are distinct named uint32 types. Zero is unset/invalid for
ImageID, RequestID, and PublicationToken. ImageID is opaque, session-unique,
never recycled, and is not a PID. RequestID correlates one logical operation.
A PublicationToken identifies one publication in one slot and is the only
valid acknowledgment value for that publication. Equal integer values do
not make the named identity types interchangeable.

## Image staging, publication, and lifetime

The host assembles the image and transfers its bytes through the existing
buffer/host-input path. A queued load has not been admitted and performs no
source read, assembly, validation, transfer, or storage work. Once admitted,
the load expires the prior ImageID immediately, before source read/assembly,
validation, or storage; a later failure therefore leaves no logically valid
image. The 64-KiB buffer is staging/transfer memory only. ZEPA's load owner
validates the transferred bytes and copies a successful image into the one
protected RAM window `[0x3FFE0000,0x3FFF0000)`. There is one 4-byte
little-endian payload-length prefix followed by at most 65,532 payload
bytes; 65,533 is invalid. The descriptor length must equal this prefix. A
pure bounds/consistency check is declared for consumers; it does not assemble
or store images.

The load owner copies all bytes and initializes descriptor metadata before
publishing its nonzero valid token last. Readers consume only a descriptor
whose valid token is nonzero and differs from the descriptor ack token. A
designated descriptor reader explicitly acks the PublicationToken after
consuming the metadata. Descriptor ack means only “metadata consumed”; it
does not permit image replacement. Logical validity is distinct from byte
lifetime: admission expires the old ImageID immediately, but an independent
reader that pinned the image before admission may finish on the unchanged
bytes. New init/exec operations reject the expired ImageID. Do not overwrite
or remove the physical bytes until every prior reader/init/exec reference
ends (or reuse the window immediately if no reference remains). A successful
load publishes a fresh, never-recycled ImageID only after copying the new
bytes. No multi-image RAM store is defined here.

## Control map and wire records

The control map begins at physical `0x3FFD0000`; kernel virtual users add the
mapping offset. The v1 map is little-endian uint32 words, and every listed
offset is relative to the control base. The bootstrap is a distinct reserved
256-byte segment `[+0x000,+0x100)`. It contains magic `0x46324331` at `+0x00`
(bytes `31 43 32 46`), version 1 at `+0x04`, capabilities (zero) at `+0x08`,
ready at `+0x0C`, control physical base at `+0x10`, image physical base at
`+0x14`, and buffer physical base at `+0x18`; all remaining bootstrap bytes
are reserved zero. The bootstrap producer clears ready before modifying any
bootstrap fields and stores ready=1 last. A consumer rejects an unready,
unknown magic, or unsupported version. Readiness is protocol initialization,
not evidence that a mailbox contains a message.

Five independent word-aligned structures follow:

| Offset / size | Owner and word fields (offset relative to structure) |
|---|---|
| `+0x100 / 0x20` status | status producer: version `+0x00` (1), phase `+0x04`, ready `+0x08` (published last), reserved `+0x0C` |
| `+0x120 / 0x20` descriptor | ZEPA loader publishes; controller/handler reader consumes: ImageID `+0x00`, payload length `+0x04`, physical image base `+0x08`, generation `+0x0C`, valid token `+0x10`, ack token `+0x14`; `+0x18..+0x1F` reserved |
| `+0x140 / 0x20` request | host or syscall-trap ingress publishes; kernel controller consumes: kind `+0x00`, ImageID `+0x04`, PID `+0x08`, v1 flags=0 `+0x0C`, valid token `+0x10`, ack token `+0x14`, RequestID `+0x18`, reserved `+0x1C` |
| `+0x160 / 0x20` result | controller publishes; designated result reader consumes: kind `+0x00`, reason `+0x04`, PID `+0x08`, ImageID `+0x0C`, RequestID `+0x10`, valid token `+0x14`, ack token `+0x18`, termination code `+0x1C` |
| `+0x180 / 0x20` event | event producer publishes; designated event reader consumes: kind `+0x00`, reason `+0x04`, PID `+0x08`, ImageID `+0x0C`, RequestID `+0x10`, valid token `+0x14`, ack token `+0x18`, reserved `+0x1C` |

The bootstrap and every slot are separate; no segment is overlaid. For every
slot, one producer owns its payload and valid token, and one designated
consumer owns its ack token. The producer waits until valid equals ack (or
both are zero), writes payload under common host/VM serialization or an
equivalent release/acquire synchronization mechanism, then publishes a new
nonzero valid token last. Readers act only when valid is nonzero and differs
from ack. Readiness and token validity are independent. Token counters are
unique per slot for one negotiated session and are never reused. Exhaustion
ends the session or requires a newly negotiated epoch; wraparound is not a
valid reuse strategy. The contract defines no implicit ack on read.

## Ownership, operation order, and backpressure

* Host reads/assembles/transfers bytes only after a load is admitted, submits
  host-originated operations, and reads only designated result/event
  streams. ZEPA validates and commits bytes to the protected single image
  window, publishes/owns image identity and lifetime, and publishes the
  descriptor. Control ingress serializes host and trap requests and allocates
  RequestIDs; kernel control consumes the request slot. Process handlers
  produce operation outcomes. The designated result and event readers
  record/handle their records, then explicitly ack their own slot tokens.
* RequestID is allocated before a request is published. Host `Submit` returns
  that already-published request's ID. The syscall trap uses the same
  serialized allocator, preventing collisions. Request ack means accepted,
  not completed. One logical operation is admitted at a time; the next is
  admitted only after terminal result recording and ack and after event
  handling/backpressure permits it.
* Result is independently published and persists by RequestID (and the
  identifying PID/ImageID fields) through its acknowledgment, even if a PCB
  has been removed. Event acknowledgment is independent of result
  acknowledgment. Exec publishes `exec-started` and exactly one terminal
  event, each with its own event PublicationToken.
* There is one event slot and at most one bounded pending event outside the
  slot (`MaxPendingEvents=1`); this is not a general queue promise. If an
  event cannot drain, the producer retains only this bounded pending state,
  never spins in a kernel handler, and stops admission until the event slot
  drains. A terminal result can be recorded without waiting for event ack.
* Diagnostic reason values are none=0, invalid-image=1,
  image-unavailable=2, no-frames=3, no-pcb=4, invalid-layout=5. Request
  kinds are load=1, init=2, exec=3; result kinds are load-ok=1,
  load-failed=2, init-created=3, init-failed=4, exec-succeeded=5,
  exec-failed=6; event kinds are init-created=1, init-failed=2,
  exec-started=3, exec-succeeded=4, exec-failed=5. Phases are
  uninitialized=0, ready=1, loading=2, initializing=3, executing=4,
  failed=5. Zero is reserved for “none” in enum fields. Termination code 3
  remains insufficient frames and code 4 is invalid reference/image; do not
  redefine existing codes 1–2 or any other syscall meaning.

### Concrete load → init → exec-failed trace (contract example only)

The following sample IDs make the publication and acknowledgment order
explicit; the numbers are illustrative session values, not allocated by
these definitions. A single operation is in flight at a time. For brevity,
`valid/ack` shows the token words in the named slot, and each next token is
new for its own slot.

1. **Load request 41.** The request producer allocates RequestID 41 before
   publishing a load request (ImageID is still zero); for example
   request-slot `valid=11, ack=10`. Kernel control accepts it and acknowledges
   token 11. This is the load's admission; any previous ImageID expires here,
   before source read or assembly. Host then transfers the 128-byte payload
   through the staging buffer. ZEPA validates the bytes, copies the
   length-prefixed payload into the protected image window, assigns ImageID
   7, and then publishes its descriptor last (descriptor `valid=8, ack=7`,
   length=128, generation=1). The designated descriptor reader consumes the
   metadata and explicitly writes descriptor `ack=8`. This ack confirms
   metadata consumption only: it does not release the image window or permit
   overwrite. The load result is published as `load-ok`, RequestID 41,
   ImageID 7 (result `valid=21, ack=20`) and is recorded/acked as token 21.
   Only after this terminal result ack may init be admitted.
2. **Init request 42.** The producer publishes init for ImageID 7 with
   RequestID 42 (request `valid=12, ack=11`); the controller accepts and
   acknowledges token 12. Its `init-created` result for PID 8 and ImageID 7
   is published at result `valid=22, ack=21`; the designated reader records
   it and acknowledges token 22. The init-created event is independently
   published and handled/acked (for example event `valid=2, ack=1`, then
   `ack=2`). Only after the result is acknowledged and event handling has
   drained may exec be admitted.
3. **Exec request 43.** The producer publishes exec for PID 8/ImageID 7
   (request `valid=13, ack=12`); acceptance is acknowledged with token 13.
   While the operation holds its image reference, image 7 remains immutable.
   The controller publishes `exec-started` with RequestID 43 and event
   `valid=3, ack=2`; the event reader has not yet acknowledged token 3.
   Exec then fails for insufficient frames: the durable result is
    `exec-failed`, reason `no-frames` (3), PID 8, ImageID 7, RequestID 43,
    termination code 3, published independently as result
    `valid=23, ack=22`. If failure cleanup removes PID 8's PCB, this result
    remains available by RequestID 43 and ImageID 7; it does not depend on the
    PCB. The result reader records and acknowledges token 23 even while the
    started event remains unacknowledged.
4. The terminal `exec-failed` event cannot overwrite the unacknowledged
   `exec-started` event. Its one bounded pending-event entry is retained
   outside the slot; the result publication and result ack above do not wait
   for that event. No handler spins, and no next request is admitted. When
   the event reader handles/acks started token 3, the producer drains the
   pending terminal event into the slot (`valid=4, ack=3`). The reader then
   handles and acknowledges terminal token 4 (`ack=4`). Only after the
   terminal result has been recorded and acked **and** event backpressure has
   drained (as well as the request acceptance ack) can another operation be
   admitted. The image reference is released when the operation/readers no
   longer hold it; descriptor ack alone never authorized replacement.

This trace specifies contract ordering and ownership only. It does not claim
that any loader, controller, event queue, service, or runtime behavior is
implemented.

### Replacement admission, failure, and pinned-reader trace (contract example only)

Assume ImageID A is currently valid. An init or exec operation on A is active,
and the host queues load B. While B is queued/unadmitted, A remains valid and
the active operation may continue using it. The queued load does not begin
reading or assembling B's source. B cannot be admitted until the active
operation has a recorded and acknowledged terminal result and any required
event backpressure has drained.

At B admission, A's ImageID expires immediately, before reading or assembling
B, validation, transfer, or storage. From this point, a newly admitted
init(A) or exec(A) is rejected, even though bytes for A may still physically
remain. An independent reader that pinned A before B was admitted may finish
on those unchanged bytes; descriptor acknowledgment did not release that
reference. If no such reader remains, the single image window is reusable
immediately. If a reader remains, B may validate but cannot overwrite the
window until that reader releases its reference.

If admitted load B fails at source read, assembly, validation, or storage,
its load-failed result is recorded and acknowledged, and there is no valid
ImageID—not A and not B. After the active load's terminal result/event gates
allow another operation, an init(A) or exec(A) request is rejected as
unavailable. If a prior reader still pins A's bytes, those bytes remain
immutable until it finishes, but they are not available as a valid image; no
multi-image store is implied. A failure before the pinned reader releases
must leave A's bytes unchanged; storage/reuse cannot begin until release.
Once that reader releases the bytes (or immediately if there was no reader),
a later admitted successful load may reuse the window. It copies the new
image completely, then publishes fresh, session-unique ImageID C in the
descriptor. The descriptor reader's ack for C confirms metadata consumption
only and does not authorize a later overwrite while C has references.

This is a definitions-only ordering example, not implemented load behavior.

## ASM helper ABI proposal

These are proposed helper conventions, not implemented helpers. They follow
the existing `map_page` return linkage: W8 carries the return address and
the helper returns with `JMPR W8`. Nested calls must save and restore W8.

| Helper | Inputs | Result in W9 |
|---|---|---|
| page release | W7 = PCB kernel virtual address; W6 = page-table virtual address | 0 success, 3 insufficient frames, or 5 invalid layout |
| event validation/publication | W7 = event-slot kernel virtual address; W6 = PublicationToken; W5 = event kind | 0 success or 5 invalid layout |

Both preserve W8, W0–W4, UPTR, KPTR, SP, and PCB parent/child/previous
sibling/next sibling fields. Both may clobber W5–W7, W9, and status flags.
Helpers validate pointers before stores and synchronize publication. These
register conventions do not add syscall or argv semantics. The scoped exec
boundary remains `SYSCALL #5` with ImageID in W8; the existing syscall
instruction places its immediate in W9. Front 1's contract remains the
authority for all other syscall and argv details.
