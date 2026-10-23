; exec_destination.asm -- destination image for the exec flow ([F2-04]).
;
; Entry context
;   Virtual address 0, first byte of the image. No _start label is required:
;   the loader transfers control to offset 0, and the first instruction below
;   is the entry point. Registers may be zeroed or stale, so this image
;   initializes every register it reads and does not rely on the previous
;   context.
;
; Inspectable markers (user virtual memory, first 4 KiB page)
;   0x0108: 0x4453 ("DS") destination marker.
;   W0:     0x4453, live copy of the destination marker.
;
; 0x0100 and 0x0104 belong to exec_intermediate.asm. This image never writes
; them. If the exec implementation keeps the old user page contents, 0x0100
; still holds 0x494E and 0x0104 remains untouched (no sentinel), which shows
; that the destination replaced the intermediate without running its sentinel.
;
; The emitted code stays below 0x0100 bytes, so the marker slot never overlaps
; it.
;
; Stable behavior for inspection: W1 grows by one on every iteration and PC
; stays inside this image.

MV W0, #0x4453      ; destination marker, distinct from the other samples
STRD W0, #0x0108    ; publish the marker for the debugger
MV W1, #0           ; explicit counter initialization (no prior context)
MV W2, #1           ; loop step
spin:
ADD W1, W1, W2      ; observable progress
JUMP spin           ; stay in the destination image
