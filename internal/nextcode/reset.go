package nextcode

// ResetWhenIdle ends the running CLI as soon as it has nothing to do (now, or when its turn
// and the owner's answers are over), so that the next message starts one with the console's
// launch as it is then (another effort, login or permission mode). The chat and the
// conversation stay.
func (d *Driver) ResetWhenIdle() {
	idle := func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return !d.status.Working && len(d.reqs) == 0
	}
	if idle() {
		d.Reset()
		return
	}
	_, sub := d.Subscribe()
	go func() {
		defer d.Unsubscribe(sub)
		for f := range sub.C {
			if f.T == FrameStatus && f.Status != nil && !f.Status.Working && idle() {
				d.Reset()
				return
			}
		}
	}()
}

// Engine is the engine the driver speaks.
func (d *Driver) Engine() string { return d.cfg.Engine }
