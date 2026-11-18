package skeleton

func (a *coreAPI) JournalSourceID() string {
	if a.core == nil {
		return ""
	}
	return a.core.JournalSourceID()
}
