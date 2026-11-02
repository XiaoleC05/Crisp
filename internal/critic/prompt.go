package critic

// BuildPrompt renders the system and user prompts sent to the critic model.
// It must not include secrets and should stay stable across calls so that the
// critic's judgement is reproducible.
func BuildPrompt(in Input) (system, user string) {
	// TODO(critic): implement.
	return "", ""
}
