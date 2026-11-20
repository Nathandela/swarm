package scheduledexec

// Prompt is the first native user message for a scheduled occurrence.
func Prompt(base string) string {
	return "Scheduled automation from Swarm\n\n" + base + "\n\n" +
		"Once the requested work is complete, record completion with the scheduled completion tool if it is available. Otherwise, call the local completion command from this session: \"$SWARM_SCHEDULE_BIN\" schedule complete --summary 'your summary'. " +
		"Use --link once per artifact if needed. After the receipt succeeds, send a concise final assistant message with the result, current state, and any artifact links. Include the receipt summary verbatim in that closing message."
}
