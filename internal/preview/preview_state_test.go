package preview

import (
	"errors"
	"fmt"
	"testing"

	"github.com/azamrizki/nextboard/internal/machine"
)

// What a dial error makes of a preview goes by the error, never by its words.
func TestStateFor(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{machine.ErrForwardingDisabled, StateError},
		{machine.ErrNotConnected, StateError},
		{fmt.Errorf("%w: eof", machine.ErrDisconnected), StateError},
		{machine.ErrNotFound, StateError},
		{fmt.Errorf("%w: connection refused", machine.ErrPortClosed), StateWaiting},
		{errors.New("dial tcp 127.0.0.1:3000: connect: connection refused"), StateWaiting},
	} {
		if got, _ := stateFor(c.err); got != c.want {
			t.Errorf("%v: %s, want %s", c.err, got, c.want)
		}
	}
}
