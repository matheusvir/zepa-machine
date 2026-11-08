; exec_intermediate.asm -- intermediate process for the exec flow ([F2-04]).
;
; Entry context
;   W8: full 32-bit ImageID chosen by the kernel/control handler. It is opaque
;       and must reach SYSCALL #5 unchanged. Never copy it into an immediate:
;       this assembler encodes MV/LDD/STRD immediates as 16 bits.
;
; Inspectable markers (user virtual memory, first 4 KiB page)
;   0x0100: 0x494E ("IN") intermediate marker, written before the call.
;   0x0104: 0x534E ("SN") sentinel, written only if SYSCALL #5 returns.
;   W0:     0x494E, live copy of the intermediate marker.
;   W1:     0x534E after a failed exec, live copy of the sentinel.
;
; The emitted code stays below 0x0100 bytes, so the marker slots never overlap
; it. Both marker values fit in a positive 16-bit immediate.
;
; Success path: exec replaces this image, so the sentinel block below is never
; executed and 0x0104 stays untouched.

MV W0, #0x494E      ; intermediate marker; W8 (ImageID) is not touched
STRD W0, #0x0100    ; publish the marker for the debugger

; W8 still holds the complete 32-bit ImageID.
SYSCALL #5          ; exec(W8); on success this image is replaced

; ---- sentinel: reachable only when exec fails and returns ----
MV W1, #0x534E      ; sentinel value, distinct from marker and ImageID
STRD W1, #0x0104    ; publish the sentinel to prove a return happened
MV W8, #1           ; exit status 1; W8 is free after the exec call
SYSCALL #2          ; exit()
