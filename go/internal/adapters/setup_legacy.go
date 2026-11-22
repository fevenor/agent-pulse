package adapters

func setupLegacyCodex(agents []setupAgent) bool {
	for _, agent := range agents {
		if agent.ID == "codex" {
			return agent.Manifest.Control == ""
		}
	}
	return false
}
