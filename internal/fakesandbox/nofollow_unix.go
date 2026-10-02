package fakesandbox

import (
	"errors"
	"syscall"
)

const noFollow = syscall.O_NOFOLLOW

func isLoop(err error) bool { return errors.Is(err, syscall.ELOOP) }
