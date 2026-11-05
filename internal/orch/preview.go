package orch

// PreviewOpener creates (or reuses) a hub preview of a localhost port on a
// machine and returns its id. The hub wires it to the preview manager.
type PreviewOpener interface {
	OpenPreview(machineID string, port int) (string, error)
}

// webTileData fills a web tile for "open localhost <port>": the project's
// machine, the port and — when the hub can open previews — the preview id,
// so clients load it without creating the preview themselves.
func (s *Service) webTileData(c *execCtx, data map[string]any) {
	port, _ := data["port"].(int)
	if port <= 0 {
		return
	}
	machineID := "local"
	if p, ok := s.hub.Project(c.projectID); ok && p.MachineID != "" {
		machineID = p.MachineID
	}
	data["machineId"] = machineID
	data["device"], data["zoom"], data["auto"] = "fit", 100, false
	if s.Previews == nil {
		return
	}
	if id, err := s.Previews.OpenPreview(machineID, port); err == nil {
		data["previewId"] = id
	}
}
