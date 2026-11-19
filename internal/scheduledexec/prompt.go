package scheduledexec

// Prompt is the first native user message for a scheduled occurrence.
func Prompt(base string) string {
	return "Scheduled automation from Swarm\n\n" + base + "\n\n" +
		"Before completing this run, post a concise closing chat message with the result, current state, and links to any artifacts. " +
		"Then record completion with the scheduled completion tool if it is available. Otherwise, call the local completion command from this session: \"$SWARM_SCHEDULE_BIN\" schedule complete --summary 'your summary'. " +
		"Use --link once per artifact if needed. Include the receipt summary verbatim in your closing chat message so Swarm can link them. Only send the receipt when the requested work is complete."
}
