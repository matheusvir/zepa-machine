_start:
    MV W1, #5
    MV W2, #3
    ADD W8, W1, W2
    ;SYSCALL #2
_hold:
    JUMP _hold
